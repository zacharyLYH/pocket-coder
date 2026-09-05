package state

import (
	"os"
	"path/filepath"
	"testing"

	"pcoder/internal/state/statetest"
)

func open(t *testing.T, dataDir string, seed Bootstrap) (*Store, Document) {
	t.Helper()
	st, err := Open(dataDir, seed)
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	st.View(func(d *Document) { doc = *d })
	return st, doc
}

func TestFreshOpenSeedsAndPersists(t *testing.T) {
	dataDir := t.TempDir()
	st, doc := open(t, dataDir, Bootstrap{
		LoginEmail: "me@example.com",
		SMTP:       &SMTP{Host: "smtp.gmail.com", Port: 587, User: "me@example.com", Password: "pw"},
	})
	if doc.User.Email != "me@example.com" {
		t.Fatalf("user email = %q", doc.User.Email)
	}
	if doc.SMTP == nil || doc.SMTP.Password != "pw" {
		t.Fatalf("smtp = %+v", doc.SMTP)
	}

	// the document is on disk immediately, and it is EXACTLY the seed:
	// no empty sections, no stray keys — nothing more, nothing less.
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
		"smtp": map[string]any{
			"host":     "smtp.gmail.com",
			"port":     587,
			"user":     "me@example.com",
			"password": "pw",
		},
	})
}

// A fresh install with nothing seeded writes an (almost) empty document.
func TestFreshOpenNoSeedIsEmptyFile(t *testing.T) {
	st, _ := open(t, t.TempDir(), Bootstrap{})
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
	})
}

// Env-derived config only fills EMPTY fields: an existing state file wins.
func TestSeedDoesNotOverrideExisting(t *testing.T) {
	dataDir := t.TempDir()
	st, _ := open(t, dataDir, Bootstrap{LoginEmail: "env@example.com"})
	if err := st.Mutate(func(doc *Document) error {
		doc.User.Email = "state@example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// reopen as if a second boot with different env
	st2, doc := open(t, dataDir, Bootstrap{LoginEmail: "other@example.com"})
	if doc.User.Email != "state@example.com" {
		t.Fatalf("seed overrode state: %q", doc.User.Email)
	}
	_ = st2
}

func TestMutatePersistsAndRollsBackOnError(t *testing.T) {
	dataDir := t.TempDir()
	st, doc := open(t, dataDir, Bootstrap{LoginEmail: "me@example.com"})
	if doc.Projects != nil {
		t.Fatal("fresh document should have no projects")
	}

	if err := st.Mutate(func(doc *Document) error {
		doc.Projects = map[string]Project{"abc": {Name: "x"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// a failed mutate must change nothing on disk or in memory
	if err := st.Mutate(func(doc *Document) error {
		doc.Projects["abc"] = Project{Name: "mutated"}
		return os.ErrPermission
	}); err == nil {
		t.Fatal("expected mutate error")
	}
	// the failed mutate left the file byte-for-byte at the last good state
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": "me@example.com"},
		"projects": map[string]any{"abc": map[string]any{"name": "x", "repo": ""}},
	})
}

func TestFreshInstallIsEmpty(t *testing.T) {
	_, doc := open(t, t.TempDir(), Bootstrap{})
	if doc.User.Email != "" || doc.Projects != nil || doc.Harnesses != nil || doc.SSHKeys != nil {
		t.Fatalf("fresh install should be empty, got %+v", doc)
	}
}

func TestCorruptStateFileFailsLoudly(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "state.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dataDir, Bootstrap{}); err == nil {
		t.Fatal("corrupt state.json must fail startup, not reset silently")
	}
}
