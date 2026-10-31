package sync

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"pcoder/internal/docker"
)

// fakeContainer records the execs Work Sync runs. StreamExec emulates a
// `tar -C dir -cf -` read (one complete archive, end-of-archive markers
// included, per call) and records the stdin each call received; other
// commands only record. Paths arrive either in argv (after "--") or, as the
// production commands do, NUL-separated on stdin.
type fakeContainer struct {
	calls  [][]string
	stdins [][]byte
}

func (f *fakeContainer) Exec(ctx context.Context, id string, cmd []string, tty bool) (docker.ExecResult, error) {
	return docker.ExecResult{}, nil
}

func (f *fakeContainer) Upload(ctx context.Context, id, dir string, r io.Reader) error { return nil }

func (f *fakeContainer) StreamExec(ctx context.Context, id string, cmd []string, stdin io.Reader, stdout io.Writer) (int, error) {
	f.calls = append(f.calls, cmd)
	var names []byte
	if stdin != nil {
		var err error
		if names, err = io.ReadAll(stdin); err != nil {
			return 0, err
		}
	}
	f.stdins = append(f.stdins, names)
	if len(cmd) == 0 || cmd[0] != "tar" {
		return 0, nil
	}
	var paths []string
	if i := slices.Index(cmd, "--"); i >= 0 {
		paths = cmd[i+1:]
	} else if s := strings.TrimRight(string(names), "\x00"); s != "" {
		paths = strings.Split(s, "\x00")
	}
	tw := tar.NewWriter(stdout)
	for _, p := range paths {
		if err := tw.WriteHeader(&tar.Header{Name: p, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(p))}); err != nil {
			return 0, err
		}
		if _, err := tw.Write([]byte(p)); err != nil {
			return 0, err
		}
	}
	return 0, tw.Close()
}

// manyPaths returns n dir-relative file names.
func manyPaths(n int) []string {
	paths := make([]string, n)
	for i := range paths {
		paths[i] = fmt.Sprintf("dir/file-%03d.txt", i)
	}
	return paths
}

// nulList joins paths the way both container-side readers expect them.
func nulList(paths []string) []byte {
	var out []byte
	for _, p := range paths {
		out = append(out, p...)
		out = append(out, 0)
	}
	return out
}

// TestContainerTarKeepsEveryPath pins the reader side of a container tar:
// every requested path must arrive in the stream. Batching the read into
// several `tar -cf -` runs concatenates complete archives, and
// archive/tar.Reader stops at the first end-of-archive marker, so paths
// beyond the batch silently vanished. 237 is past the 200 the old batch
// used.
func TestContainerTarKeepsEveryPath(t *testing.T) {
	paths := manyPaths(237)

	fc := &fakeContainer{}
	r := containerTar(context.Background(), fc, "pcoder-acme-api", "/work", paths)
	raw, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())

	seen := 0
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		_, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		seen++
	}
	require.Equal(t, len(paths), seen, "container tar stopped after the first archive; paths past the batch were lost")
	require.Len(t, fc.calls, 1, "the read path must stream every path in one tar invocation")
	require.Equal(t, nulList(paths), fc.stdins[0], "paths must ride stdin, not argv")
}

// TestRemovePathsSendsEveryPathInOneExec pins the delete path to the same
// contract: one exec, names on stdin, so argv never grows and no batching is
// needed.
func TestRemovePathsSendsEveryPathInOneExec(t *testing.T) {
	paths := manyPaths(237)

	fc := &fakeContainer{}
	require.NoError(t, RemovePaths(context.Background(), fc, "pcoder-acme-api", "/work", paths))

	require.Len(t, fc.calls, 1, "one exec deletes every path")
	require.Equal(t, nulList(paths), fc.stdins[0], "paths must ride stdin, not argv")
}
