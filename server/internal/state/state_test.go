package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pcoder/internal/auth"
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
	st, doc := open(t, dataDir, Bootstrap{AuthEmail: "me@example.com"})
	if doc.User.Email != "me@example.com" {
		t.Fatalf("user email = %q", doc.User.Email)
	}

	// the document is on disk immediately, and it is EXACTLY the seed:
	// no empty sections, no stray keys — nothing more, nothing less.
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": "me@example.com"},
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
	st, _ := open(t, dataDir, Bootstrap{AuthEmail: "env@example.com"})
	if err := st.Mutate(func(doc *Document) error {
		doc.User.Email = "state@example.com"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// reopen as if a second boot with different env
	st2, doc := open(t, dataDir, Bootstrap{AuthEmail: "other@example.com"})
	if doc.User.Email != "state@example.com" {
		t.Fatalf("seed overrode state: %q", doc.User.Email)
	}
	_ = st2
}

func TestMutatePersistsAndRollsBackOnError(t *testing.T) {
	dataDir := t.TempDir()
	st, doc := open(t, dataDir, Bootstrap{AuthEmail: "me@example.com"})
	if doc.Projects != nil {
		t.Fatal("fresh document should have no projects")
	}

	if err := st.Mutate(func(doc *Document) error {
		doc.Projects = map[string]Project{"x/hello": {Repo: "https://github.com/x/hello.git"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// a failed mutate must change nothing on disk or in memory
	if err := st.Mutate(func(doc *Document) error {
		doc.Projects["x/hello"] = Project{Repo: "https://github.com/x/other.git"}
		return os.ErrPermission
	}); err == nil {
		t.Fatal("expected mutate error")
	}
	// the failed mutate left the file byte-for-byte at the last good state
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user":     map[string]any{"email": "me@example.com"},
		"projects": map[string]any{"x/hello": map[string]any{"repo": "https://github.com/x/hello.git"}},
	})
}

func TestFreshInstallIsEmpty(t *testing.T) {
	_, doc := open(t, t.TempDir(), Bootstrap{})
	if doc.User.Email != "" || doc.Projects != nil || doc.Harnesses != nil || doc.ServerKey != nil {
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

// SetPassword stores only the salted Argon2id hash (never plaintext), syncs
// the setup email, bumps the session generation, and survives reopen — the
// three spec invariants for the credential: hashed, versioned, durable.
func TestSetPasswordPersistsHashAndBumpsVersion(t *testing.T) {
	restore := auth.TestParams()
	defer restore()
	dataDir := t.TempDir()
	st, _ := open(t, dataDir, Bootstrap{})

	hash, err := auth.HashPassword("hunter2-super-secret")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword("owner@example.com", hash); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPassword("owner@example.com", hash); err != nil {
		t.Fatal(err)
	}

	var doc Document
	st.View(func(d *Document) { doc = *d })
	if doc.User.AuthVersion != 2 {
		t.Fatalf("authVersion = %d, want 2 (one bump per apply)", doc.User.AuthVersion)
	}
	if doc.User.Email != "owner@example.com" {
		t.Fatalf("email = %q", doc.User.Email)
	}

	// plaintext never reaches the file; only the PHC hash does
	raw, err := os.ReadFile(st.Path())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2-super-secret") {
		t.Fatal("plaintext password persisted in state.json")
	}
	if !strings.Contains(string(raw), "$argon2id$") {
		t.Fatalf("no argon2id hash in state.json:\n%s", raw)
	}

	// durable: a reopen (restart) still verifies against the same hash
	st2, _ := open(t, dataDir, Bootstrap{})
	var doc2 Document
	st2.View(func(d *Document) { doc2 = *d })
	if doc2.User.PasswordHash != hash {
		t.Fatal("password hash did not survive reopen")
	}
}

// The data dir holds the credential: owner-only, even when it already
// existed with looser bits.
func TestOpenEnforcesDirPermissions(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dataDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dataDir, Bootstrap{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data dir perms = %o, want 700", info.Mode().Perm())
	}
}
