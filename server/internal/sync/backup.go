package sync

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// BackupMeta is manifest.json inside a backup archive. It records the sync
// identity plus one entry per affected path, so restore can reverse the
// recorded actions without the server.
type BackupMeta struct {
	Project   string        `json:"project"`
	Branch    string        `json:"branch"`
	Commit    string        `json:"commit"`
	Timestamp string        `json:"timestamp"`
	Entries   []BackupEntry `json:"entries"`
}

// BackupEntry is one affected path in a backup archive.
type BackupEntry struct {
	Path   string `json:"path"`
	Action string `json:"action"`
	Mode   uint32 `json:"mode"`
	Type   string `json:"type"`
	Hash   string `json:"hash"`
}

// BackupStamp is the timestamp format used in archive names.
const BackupStamp = "2006-01-02T15-04-05"

// ProjectSlug turns owner/repo into a filesystem-safe directory name.
func ProjectSlug(project string) string {
	return strings.NewReplacer("/", "-", "\\", "-", ":", "-", " ", "-").Replace(project)
}

// BackupPath returns the archive path for a project and timestamp, adding a
// numeric suffix if two syncs share a second.
func BackupPath(root, project string, at time.Time) string {
	dir := filepath.Join(root, ProjectSlug(project))
	base := at.Format(BackupStamp)
	p := filepath.Join(dir, base+".tar.gz")
	for i := 2; ; i++ {
		if _, err := os.Stat(p); os.IsNotExist(err) {
			return p
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d.tar.gz", base, i))
	}
}

// WriteBackup archives the affected paths that already exist under dir.
// target is the pre-sync manifest of dir; ops are the planned changes, so
// only paths the sync will touch are copied. It returns the archive path.
func WriteBackup(root, project, branch, commit, dir string, target Manifest, ops []Op, at time.Time) (string, error) {
	byPath := target.Index()
	meta := BackupMeta{Project: project, Branch: branch, Commit: commit, Timestamp: at.UTC().Format(time.RFC3339)}
	for _, op := range ops {
		if op.Action == ActionAdded {
			// Nothing existed to save, but restore still needs the path so it
			// can delete what the sync added.
			meta.Entries = append(meta.Entries, BackupEntry{Path: op.Path, Action: ActionAdded})
			continue
		}
		e, ok := byPath[op.Path]
		if !ok {
			continue
		}
		meta.Entries = append(meta.Entries, BackupEntry{Path: op.Path, Action: op.Action, Mode: e.Mode, Type: e.Type, Hash: e.Hash})
	}
	dst := BackupPath(root, project, at)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	if err := writeTarBytes(tw, "manifest.json", 0o600, raw); err != nil {
		return "", err
	}
	for _, be := range meta.Entries {
		if be.Action == ActionAdded {
			continue
		}
		src := filepath.Join(dir, filepath.FromSlash(be.Path))
		if err := WriteTarEntry(tw, path.Join("files", be.Path), src, be.Type, be.Mode); err != nil {
			return "", fmt.Errorf("backup %s: %w", be.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return dst, nil
}

func writeTarBytes(tw *tar.Writer, name string, mode int64, b []byte) error {
	if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: mode, Size: int64(len(b))}); err != nil {
		return err
	}
	_, err := tw.Write(b)
	return err
}

// ExtractBackup unpacks an archive into dst and returns its manifest. Every
// file listed in the manifest is hash-verified.
func ExtractBackup(archive, dst string) (BackupMeta, error) {
	f, err := os.Open(archive)
	if err != nil {
		return BackupMeta{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return BackupMeta{}, fmt.Errorf("open backup: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var meta BackupMeta
	haveMeta := false
	hashes := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return BackupMeta{}, fmt.Errorf("read backup: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "manifest.json" {
			if err := json.NewDecoder(tr).Decode(&meta); err != nil {
				return BackupMeta{}, fmt.Errorf("read backup manifest: %w", err)
			}
			haveMeta = true
			continue
		}
		if !strings.HasPrefix(name, "files/") {
			return BackupMeta{}, fmt.Errorf("backup contains unexpected path %q", name)
		}
		rel := strings.TrimPrefix(name, "files/")
		if !ValidPath(rel) {
			return BackupMeta{}, fmt.Errorf("backup contains unsafe path %q", rel)
		}
		out := filepath.Join(dst, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeSymlink:
			if filepath.IsAbs(h.Linkname) {
				return BackupMeta{}, fmt.Errorf("backup contains unsafe symlink %q", rel)
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return BackupMeta{}, err
			}
			if err := os.Symlink(h.Linkname, out); err != nil {
				return BackupMeta{}, err
			}
			hashes[rel] = HashBytes([]byte(h.Linkname))
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return BackupMeta{}, err
			}
			w, err := os.OpenFile(out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(h.Mode))
			if err != nil {
				return BackupMeta{}, err
			}
			hsh := sha256.New()
			if _, err := io.Copy(io.MultiWriter(w, hsh), tr); err != nil {
				w.Close()
				return BackupMeta{}, err
			}
			w.Close()
			hashes[rel] = hex.EncodeToString(hsh.Sum(nil))
		}
	}
	if !haveMeta {
		return BackupMeta{}, fmt.Errorf("backup is missing manifest.json")
	}
	for _, e := range meta.Entries {
		if e.Action == ActionAdded {
			continue
		}
		if got, ok := hashes[e.Path]; !ok {
			return BackupMeta{}, fmt.Errorf("backup is missing %q", e.Path)
		} else if got != e.Hash {
			return BackupMeta{}, fmt.Errorf("backup hash mismatch for %q", e.Path)
		}
	}
	return meta, nil
}

// PruneBackups keeps the newest keep archives per project.
func PruneBackups(root, project string, keep int) {
	dir := filepath.Join(root, ProjectSlug(project))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tar.gz") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// Compare reports the first difference between what a sync expected and
// what a folder actually holds.
func Compare(want, got Manifest) error {
	wantIdx, gotIdx := want.Index(), got.Index()
	if len(wantIdx) != len(gotIdx) {
		return fmt.Errorf("manifest names %d files, folder has %d", len(wantIdx), len(gotIdx))
	}
	for p, e := range wantIdx {
		g, ok := gotIdx[p]
		if !ok || g.Hash != e.Hash || g.Mode != e.Mode || g.Type != e.Type {
			return fmt.Errorf("%s did not match", p)
		}
	}
	return nil
}

// ApplyLocal copies staged entries into dir and applies the plan's deletes.
// The staged tree was hash-verified when it was written.
func ApplyLocal(dir, staged string, source Manifest, ops []Op) error {
	byPath := source.Index()
	for _, op := range ops {
		target := filepath.Join(dir, filepath.FromSlash(op.Path))
		if op.Action == ActionDeleted {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		e := byPath[op.Path]
		src := filepath.Join(staged, filepath.FromSlash(op.Path))
		if err := copyEntry(src, target, BackupEntry{Type: e.Type, Mode: e.Mode}); err != nil {
			return err
		}
	}
	return nil
}

// RestoreLocal reverses a backup archive in dir: added paths are deleted,
// overwritten and deleted paths are recreated from the archive, and hashes
// are verified afterwards. Unrelated files are untouched.
func RestoreLocal(archive, dir string) (BackupMeta, error) {
	tmp, err := os.MkdirTemp("", "pcoder-sync-restore-")
	if err != nil {
		return BackupMeta{}, err
	}
	defer os.RemoveAll(tmp)
	meta, err := ExtractBackup(archive, tmp)
	if err != nil {
		return BackupMeta{}, err
	}
	for _, e := range meta.Entries {
		target := filepath.Join(dir, filepath.FromSlash(e.Path))
		switch e.Action {
		case ActionAdded:
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return meta, err
			}
		case ActionOverwritten, ActionDeleted:
			staged := filepath.Join(tmp, filepath.FromSlash(e.Path))
			if err := copyEntry(staged, target, e); err != nil {
				return meta, err
			}
		}
		if e.Action != ActionAdded {
			got, err := hashPath(target, e.Type)
			if err != nil {
				return meta, err
			}
			if got != e.Hash {
				return meta, fmt.Errorf("restore of %q did not verify", e.Path)
			}
		}
	}
	return meta, nil
}

func copyEntry(src, dst string, e BackupEntry) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if e.Type == TypeSymlink {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(e.Mode))
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func hashPath(p, typ string) (string, error) {
	if typ == TypeSymlink {
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		return HashBytes([]byte(target)), nil
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
