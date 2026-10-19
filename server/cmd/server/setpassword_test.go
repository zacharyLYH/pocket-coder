package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pcoder/internal/state"
)

// set-password is the setup script's setter: password in on stdin, only a
// salted Argon2id hash out to state.json, session generation bumped on
// every run (so rerunning setup with a new password kills old cookies).
func TestSetPasswordMainStoresHashAndBumpsVersion(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("PCODER_AUTH_EMAIL", "owner@example.com")
	t.Setenv("PCODER_DATA_DIR", dataDir)
	t.Chdir(t.TempDir()) // keep config.Load from picking up any repo .env

	feed := func(password string) {
		t.Helper()
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdin
		os.Stdin = r
		go func() {
			_, _ = w.Write([]byte(password))
			_ = w.Close()
		}()
		setPasswordMain()
		os.Stdin = old
	}

	feed("first-password-123")
	read := func() state.User {
		t.Helper()
		st, err := state.Open(dataDir, state.Bootstrap{})
		if err != nil {
			t.Fatal(err)
		}
		var u state.User
		st.View(func(doc *state.Document) { u = doc.User })
		return u
	}
	u := read()
	if u.Email != "owner@example.com" {
		t.Fatalf("email = %q", u.Email)
	}
	if u.AuthVersion != 1 {
		t.Fatalf("authVersion = %d, want 1", u.AuthVersion)
	}
	if !strings.HasPrefix(u.PasswordHash, "$argon2id$") {
		t.Fatalf("hash = %q, want argon2id PHC", u.PasswordHash)
	}
	// plaintext must not be anywhere in the persisted file
	raw, err := os.ReadFile(filepath.Join(dataDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "first-password-123") {
		t.Fatal("plaintext password persisted")
	}

	// rerun with a new password: hash replaced, generation bumped — the
	// spec's "every setup run applies its password" invariant.
	oldHash := u.PasswordHash
	feed("second-password-456")
	u = read()
	if u.AuthVersion != 2 {
		t.Fatalf("authVersion = %d, want 2", u.AuthVersion)
	}
	if u.PasswordHash == oldHash {
		t.Fatal("password hash unchanged after rerun")
	}
}
