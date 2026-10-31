package sync

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"pcoder/internal/docker"
)

// ContainerIO is the Docker slice Work Sync needs from a project container.
// *docker.Docker satisfies it; tests use a fake.
type ContainerIO interface {
	Exec(ctx context.Context, id string, cmd []string, tty bool) (docker.ExecResult, error)
	StreamExec(ctx context.Context, id string, cmd []string, stdin io.Reader, stdout io.Writer) (int, error)
	Upload(ctx context.Context, id, dir string, r io.Reader) error
}

// manifestScript prints one NUL-terminated record per covered file:
// path, size, octal mode, type, sha256. Only paths that still exist are
// emitted, so a tracked file deleted from the work tree drops out and
// becomes a delete on the other side.
const manifestScript = `cd -- "$1" || exit 3
git ls-files -z -co --exclude-standard | while IFS= read -r -d "" p; do
  if [ -L "$p" ]; then
    printf '%s\0%d\0%s\0%s\0%s\0' "$p" 0 "$(stat -c %a -- "$p")" l "$(readlink -- "$p" | sha256sum | cut -d" " -f1)"
  elif [ -f "$p" ]; then
    printf '%s\0%s\0%s\0%s\0%s\0' "$p" "$(stat -c %s -- "$p")" "$(stat -c %a -- "$p")" f "$(sha256sum -- "$p" | cut -d" " -f1)"
  fi
done`

// ContainerManifest builds the covered manifest inside a project container.
func ContainerManifest(ctx context.Context, c ContainerIO, container, dir string) (Manifest, error) {
	res, err := c.Exec(ctx, container, []string{"bash", "-lc", manifestScript, "pcoder-sync", dir}, false)
	if err != nil {
		return Manifest{}, fmt.Errorf("build manifest: %w", err)
	}
	if res.ExitCode != 0 {
		return Manifest{}, fmt.Errorf("build manifest in %s: exit %d: %s", container, res.ExitCode, strings.TrimSpace(res.Output))
	}
	m, err := parseManifest(res.Output)
	if err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func parseManifest(out string) (Manifest, error) {
	fields := strings.Split(out, "\x00")
	// A trailing NUL leaves one empty field; drop it.
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	if len(fields)%5 != 0 {
		return Manifest{}, fmt.Errorf("manifest output has %d fields, want a multiple of 5", len(fields))
	}
	m := Manifest{Files: make([]Entry, 0, len(fields)/5)}
	for i := 0; i < len(fields); i += 5 {
		size, err := strconv.ParseInt(fields[i+1], 10, 64)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest size for %q: %w", fields[i], err)
		}
		mode, err := strconv.ParseUint(fields[i+2], 8, 32)
		if err != nil {
			return Manifest{}, fmt.Errorf("manifest mode for %q: %w", fields[i], err)
		}
		typ := TypeFile
		if fields[i+3] == "l" {
			typ = TypeSymlink
		}
		m.Files = append(m.Files, Entry{Path: fields[i], Size: size, Mode: uint32(mode), Type: typ, Hash: fields[i+4]})
	}
	if err := m.Normalize(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

// ContainerGit reads the branch and commit of the project work tree.
func ContainerGit(ctx context.Context, c ContainerIO, container, dir string) (branch, commit string, err error) {
	res, err := c.Exec(ctx, container, []string{"bash", "-lc", `cd -- "$1" && git rev-parse --abbrev-ref HEAD && git rev-parse HEAD`, "pcoder-sync", dir}, false)
	if err != nil {
		return "", "", fmt.Errorf("read git state: %w", err)
	}
	if res.ExitCode != 0 {
		return "", "", NotGitRepo()
	}
	lines := strings.Split(strings.TrimSpace(res.Output), "\n")
	if len(lines) < 2 {
		return "", "", NotGitRepo()
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), nil
}

// containerTar streams a tar of the given dir-relative paths. The reader is
// consumed as the command writes; callers must close it.
func containerTar(ctx context.Context, c ContainerIO, container, dir string, paths []string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		err := writeContainerTar(ctx, c, container, dir, paths, pw)
		_ = pw.CloseWithError(err)
	}()
	return pr
}

// pathList encodes paths as the NUL-separated list the container-side
// readers (tar -T -, the delete loop) consume from stdin. Nothing rides
// argv, so the path count is unbounded.
func pathList(paths []string) io.Reader {
	var list bytes.Buffer
	for _, p := range paths {
		list.WriteString(p)
		list.WriteByte(0)
	}
	return &list
}

// writeContainerTar streams one tar of every path. The list goes in over
// stdin (-T -) instead of argv; batching would concatenate complete
// archives, and tar readers stop at the first end-of-archive marker,
// silently losing every later file.
func writeContainerTar(ctx context.Context, c ContainerIO, container, dir string, paths []string, w io.Writer) error {
	if len(paths) == 0 {
		return nil
	}
	// --null: names are NUL-terminated, so any name is safe verbatim.
	cmd := []string{"tar", "-C", dir, "--null", "-cf", "-", "-T", "-"}
	if _, err := c.StreamExec(ctx, container, cmd, pathList(paths), w); err != nil {
		return fmt.Errorf("read files from %s: %w", container, err)
	}
	return nil
}

// UploadFiles streams entries (already staged under root) into the
// container's dir as one tar, preserving modes and symlinks.
func UploadFiles(ctx context.Context, c ContainerIO, container, dir, root string, files []Entry) error {
	if len(files) == 0 {
		return nil
	}
	pr, pw := io.Pipe()
	go func() {
		tw := tar.NewWriter(pw)
		var err error
		for _, e := range files {
			if err = WriteTarEntry(tw, e.Path, filepath.Join(root, filepath.FromSlash(e.Path)), e.Type, e.Mode); err != nil {
				break
			}
		}
		if cerr := tw.Close(); err == nil {
			err = cerr
		}
		_ = pw.CloseWithError(err)
	}()
	if err := c.Upload(ctx, container, dir, pr); err != nil {
		_ = pr.Close()
		return fmt.Errorf("upload files to %s: %w", container, err)
	}
	return nil
}

// RemovePaths deletes paths (dir-relative) inside the container, reading the
// list from stdin like the tar read. Deleted directories are left in place:
// git does not track empty directories, so they are not covered state.
func RemovePaths(ctx context.Context, c ContainerIO, container, dir string, paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	const script = `cd -- "$1" || exit 3; while IFS= read -r -d "" p; do rm -f -- "$p" || exit 4; done`
	if _, err := c.StreamExec(ctx, container, []string{"bash", "-lc", script, "pcoder-sync", dir}, pathList(paths), io.Discard); err != nil {
		return fmt.Errorf("delete paths in %s: %w", container, err)
	}
	return nil
}

// WriteStaged verifies an incoming tar against entries and stages those
// files under root. Strict: nothing outside the plan is accepted.
func WriteStaged(r io.Reader, root string, entries []Entry) error {
	return copyTarMembers(r, root, entries, true)
}
