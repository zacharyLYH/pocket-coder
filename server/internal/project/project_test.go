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
	p := Project{Name: "hello", Repo: "https://github.com/x/hello", Branch: "main"}

	if err := s.Create("abc", p); err != nil {
		t.Fatalf("create: %v", err)
	}
	// on disk: exactly this project, exactly these fields — set fields only
	// (no empty branch/cloneMethod), nothing else in the document
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"abc": map[string]any{"name": "hello", "repo": "https://github.com/x/hello", "branch": "main"}},
	})

	got, err := s.Get("abc")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "hello" || got.Repo != p.Repo || got.Branch != "main" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	p.Name = "hello2"
	if err := s.Update("abc", p); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, _ := s.Get("abc"); got.Name != "hello2" {
		t.Fatalf("update not applied: %+v", got)
	}
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"abc": map[string]any{"name": "hello2", "repo": "https://github.com/x/hello", "branch": "main"}},
	})

	entries, err := s.List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "abc" || entries[0].Name != "hello2" {
		t.Fatalf("index wrong: %+v", entries)
	}

	if err := s.Delete("abc"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.Get("abc"); err == nil {
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
	if err := s.Create("abc", Project{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("abc", Project{Name: "y"}); err != nil {
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

// A blank project persists with name+repo only — no omitempty noise.
func TestCreateBlankProjectExactFile(t *testing.T) {
	st, s := newStore(t)
	if err := s.Create("abc", Project{Name: "untitled"}); err != nil {
		t.Fatal(err)
	}
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": ""},
		"projects": map[string]any{"abc": map[string]any{"name": "untitled", "repo": ""}},
	})
}

// List ordering is stable across calls: name, then id as tiebreak.
func TestListStableOrder(t *testing.T) {
	_, s := newStore(t)
	for _, id := range []string{"b", "a", "c"} {
		if err := s.Create(id, Project{Name: "untitled"}); err != nil {
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
