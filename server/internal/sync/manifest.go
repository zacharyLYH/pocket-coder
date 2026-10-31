// Package sync implements Work Sync: mirroring a project's covered working
// tree between a laptop and a pcoder project container without git commits,
// pushes, or pulls. The server and the pcoder-sync client share this package,
// so the manifest, delta, backup, and frame codecs have exactly one
// implementation.
package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// File types in a manifest.
const (
	TypeFile    = "file"
	TypeSymlink = "symlink"
)

// Actions recorded by a sync and by a backup archive.
const (
	ActionAdded       = "added"
	ActionOverwritten = "overwritten"
	ActionDeleted     = "deleted"
)

// Entry is one covered file: its path (slash-separated, relative), size,
// permission bits, type, and the SHA-256 of its bytes (or of the symlink
// target).
type Entry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Mode uint32 `json:"mode"`
	Type string `json:"type"`
	Hash string `json:"hash"`
}

// Manifest is the covered working tree of one side. Order is path-sorted
// after Normalize, so the same tree always serializes identically.
type Manifest struct {
	Files []Entry `json:"files"`
}

// Op is one path a sync will change on its target. Action names the
// change on the target, not on the source.
type Op struct {
	Path   string `json:"path"`
	Action string `json:"action"`
}

// Index maps path to entry.
func (m Manifest) Index() map[string]Entry {
	out := make(map[string]Entry, len(m.Files))
	for _, e := range m.Files {
		out[e.Path] = e
	}
	return out
}

// Normalize validates every path and sorts the entries. A malformed
// manifest from either side is refused: Work Sync never writes outside the
// project root.
func (m *Manifest) Normalize() error {
	seen := make(map[string]bool, len(m.Files))
	for _, e := range m.Files {
		switch {
		case !ValidPath(e.Path):
			return fmt.Errorf("invalid manifest path %q", e.Path)
		case e.Type != TypeFile && e.Type != TypeSymlink:
			return fmt.Errorf("invalid manifest type %q for %q", e.Type, e.Path)
		case len(e.Hash) != sha256.Size*2:
			return fmt.Errorf("invalid manifest hash for %q", e.Path)
		case seen[e.Path]:
			return fmt.Errorf("duplicate manifest path %q", e.Path)
		}
		seen[e.Path] = true
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	return nil
}

// ValidPath rejects empty, absolute, parent-escaping, .git, and
// empty-segment paths. Git never lists .git, so seeing one means the caller
// built the list by hand.
func ValidPath(p string) bool {
	if p == "" || len(p) > 1024 || strings.HasPrefix(p, "/") || strings.ContainsRune(p, 0) {
		return false
	}
	if p == ".git" || strings.HasPrefix(p, ".git/") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// Diff returns the ops that turn target into source: paths only in source
// are added, paths only in target are deleted, and differing paths are
// overwritten. Ops are path-sorted.
func Diff(source, target Manifest) []Op {
	src, dst := source.Index(), target.Index()
	ops := make([]Op, 0, len(src)+len(dst))
	for p, se := range src {
		te, ok := dst[p]
		switch {
		case !ok:
			ops = append(ops, Op{Path: p, Action: ActionAdded})
		case se.Hash != te.Hash || se.Mode != te.Mode || se.Type != te.Type:
			ops = append(ops, Op{Path: p, Action: ActionOverwritten})
		}
	}
	for p := range dst {
		if _, ok := src[p]; !ok {
			ops = append(ops, Op{Path: p, Action: ActionDeleted})
		}
	}
	sort.Slice(ops, func(i, j int) bool { return ops[i].Path < ops[j].Path })
	return ops
}

// Counts summarizes ops for the overwrite confirmation.
func Counts(ops []Op) (add, overwrite, deleted int) {
	for _, op := range ops {
		switch op.Action {
		case ActionAdded:
			add++
		case ActionOverwritten:
			overwrite++
		case ActionDeleted:
			deleted++
		}
	}
	return add, overwrite, deleted
}

// TransferSet is the source entries whose bytes must move for ops: every
// added or overwritten path. Deleted paths carry no bytes.
func TransferSet(source Manifest, ops []Op) []Entry {
	byPath := source.Index()
	var out []Entry
	for _, op := range ops {
		if op.Action == ActionDeleted {
			continue
		}
		if e, ok := byPath[op.Path]; ok {
			out = append(out, e)
		}
	}
	return out
}

// ErrNotRepo means dir is not inside a git work tree.
var ErrNotRepo = errors.New("not a git repository")

// GitInfo is what both sides must agree on before a sync.
type GitInfo struct {
	Project  string
	Branch   string
	Commit   string
	Detached bool
}

// GitInfoFor reads branch, commit, and origin project from dir. Local git is
// only ever read: Work Sync never commits, pushes, pulls, or stashes.
func GitInfoFor(dir string) (GitInfo, error) {
	branch, err := gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return GitInfo{}, fmt.Errorf("%w: %v", ErrNotRepo, err)
	}
	commit, err := gitOut(dir, "rev-parse", "HEAD")
	if err != nil {
		return GitInfo{}, fmt.Errorf("%w: %v", ErrNotRepo, err)
	}
	remote, err := gitOut(dir, "remote", "get-url", "origin")
	if err != nil {
		return GitInfo{}, fmt.Errorf("remote: %w", err)
	}
	return GitInfo{
		Project:  ProjectFromRemote(remote),
		Branch:   branch,
		Commit:   commit,
		Detached: branch == "HEAD",
	}, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ProjectFromRemote extracts owner/repo from the remote URL forms git
// accepts: ssh, https, git, and file paths. The last two path segments win,
// which is how a project id is derived everywhere else.
func ProjectFromRemote(remote string) string {
	s := strings.TrimSpace(remote)
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		} else {
			return ""
		}
	} else if i := strings.Index(s, ":"); i >= 0 {
		// scp-like: git@host:owner/repo
		s = s[i+1:]
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// CoveredPaths lists the tracked and untracked files git reports as not
// ignored. Paths that no longer exist are dropped, so a tracked file deleted
// from the working tree stays out of the manifest (and becomes a delete on
// the other side).
func CoveredPaths(dir string) ([]string, error) {
	cmd := exec.Command("git", "-C", dir, "ls-files", "-z", "-co", "--exclude-standard")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotRepo, err)
	}
	var paths []string
	for _, p := range strings.Split(string(out), "\x00") {
		if p == "" || !ValidPath(p) {
			continue
		}
		if _, err := os.Lstat(path.Join(dir, p)); err != nil {
			continue
		}
		paths = append(paths, p)
	}
	return paths, nil
}

// BuildManifest hashes the covered working tree under dir.
func BuildManifest(dir string) (Manifest, error) {
	paths, err := CoveredPaths(dir)
	if err != nil {
		return Manifest{}, err
	}
	m := Manifest{Files: make([]Entry, 0, len(paths))}
	for _, p := range paths {
		e, err := entryFor(dir, p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // raced a delete; the next manifest catches up
			}
			return Manifest{}, err
		}
		m.Files = append(m.Files, e)
	}
	if err := m.Normalize(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func entryFor(dir, p string) (Entry, error) {
	full := filepath.Join(dir, filepath.FromSlash(p))
	fi, err := os.Lstat(full)
	if err != nil {
		return Entry{}, err
	}
	if fi.IsDir() {
		return Entry{}, os.ErrNotExist
	}
	typ := TypeFile
	if fi.Mode()&os.ModeSymlink != 0 {
		typ = TypeSymlink
	}
	sum, err := hashPath(full, typ)
	if err != nil {
		return Entry{}, err
	}
	var size int64
	if typ == TypeFile {
		size = fi.Size()
	}
	return Entry{Path: p, Size: size, Mode: uint32(fi.Mode().Perm()), Type: typ, Hash: sum}, nil
}

// HashBytes is the manifest hash of a byte slice (small files and tests).
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
