package project

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"pcoder/internal/state"
	"pcoder/internal/state/statetest"
)

func newStore(t *testing.T) (*state.Store, *StateStore) {
	t.Helper()
	st, err := state.Open(filepath.Join(t.TempDir(), "data"), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st, Open(st)
}

func TestCRUD(t *testing.T) {
	st, s := newStore(t)
	p := Project{Repo: "https://github.com/x/hello", Branch: "main"}

	if err := s.Create("x/hello", p); err != nil {
		t.Fatalf("create: %v", err)
	}
	// on disk: exactly this project, exactly these fields — set fields only
	// (no empty cloneMethod), nothing else in the document
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"x/hello": map[string]any{"repo": "https://github.com/x/hello", "branch": "main"}},
	})

	got, err := s.Get("x/hello")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Repo != p.Repo || got.Branch != "main" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	p.Branch = "dev"
	if err := s.Update("x/hello", p); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := s.Get("x/hello"); got.Branch != "dev" {
		t.Fatalf("update not applied: %+v", got)
	}
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"x/hello": map[string]any{"repo": "https://github.com/x/hello", "branch": "dev"}},
	})

	entries, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "x/hello" {
		t.Fatalf("index wrong: %+v", entries)
	}

	if err := s.Delete("x/hello"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get("x/hello"); err == nil {
		t.Fatal("get after delete should fail")
	}
	if entries, _ := s.List(); len(entries) != 0 {
		t.Fatalf("index not updated after delete: %+v", entries)
	}
	// the projects section is gone entirely once the last project is deleted
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
	})
}

func TestGetUnknownIsNotExist(t *testing.T) {
	_, s := newStore(t)
	_, err := s.Get("ghost")
	if !os.IsNotExist(err) && err == nil {
		t.Fatalf("err = %v, want an os.ErrNotExist-wrapped error", err)
	}
}

// The state file is written atomically with owner-only permissions, and no
// .tmp files are ever left behind.
func TestFileModesAndNoTempLeftovers(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	st, err := state.Open(dataDir, state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	s := Open(st)
	if err := s.Create("x/hello", Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("x/hello", Project{Repo: "https://github.com/x/hello.git", Branch: "dev"}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dataDir, "state.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s perms = %o, want 600", path, info.Mode().Perm())
	}

	filepath.WalkDir(dataDir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(p, ".tmp") {
			t.Fatalf("leftover temp file: %s", p)
		}
		return nil
	})
}

// A minimal project persists with repo only — no omitempty noise.
func TestCreateMinimalProjectExactFile(t *testing.T) {
	st, s := newStore(t)
	if err := s.Create("x/hello", Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"x/hello": map[string]any{"repo": "https://github.com/x/hello.git"}},
	})
}

// List ordering is stable across calls: sorted by id.
func TestListStableOrder(t *testing.T) {
	_, s := newStore(t)
	for _, id := range []string{"b/repo", "a/repo", "c/repo"} {
		if err := s.Create(id, Project{Repo: "https://github.com/" + id}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	second, _ := s.List()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("list order unstable: %+v vs %+v", first, second)
	}
}
