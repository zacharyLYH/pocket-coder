package sshkeys

import (
	"os/exec"
	"strings"
	"testing"

	"pcoder/internal/state"
)

func newState(t *testing.T) (*state.Store, *Store) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not available")
	}
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st, New(st)
}

func TestEnsureGeneratesOnce(t *testing.T) {
	st, s := newState(t)
	if _, ok := s.Get(); ok {
		t.Fatal("expected no key before ensure")
	}
	first, err := s.EnsureKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first.PublicKey, "ssh-ed25519 ") {
		t.Fatalf("publicKey = %q, want ssh-ed25519", first.PublicKey)
	}
	if !strings.HasPrefix(first.PrivateKey, "-----BEGIN OPENSSH PRIVATE KEY-----") {
		t.Fatal("private key is not OpenSSH format")
	}
	if !strings.HasPrefix(first.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q, want OpenSSH SHA256 form", first.Fingerprint)
	}
	if first.CreatedAt == "" {
		t.Fatal("empty createdAt")
	}
	// idempotent: second ensure returns the identical pair (setup re-runs
	// must never rotate the key out from under GitHub).
	second, err := s.EnsureKeypair()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("ensure rotated an existing keypair")
	}
	// persisted: a fresh store over the same file sees the same key.
	st2, err := state.Open(dirOf(t, st), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	got, ok := New(st2).Get()
	if !ok || got != first {
		t.Fatalf("keypair not persisted: %+v", got)
	}
}

func TestRegenerateRotates(t *testing.T) {
	_, s := newState(t)
	first, err := s.EnsureKeypair()
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Regenerate()
	if err != nil {
		t.Fatal(err)
	}
	if second.PublicKey == first.PublicKey || second.PrivateKey == first.PrivateKey {
		t.Fatal("regenerate kept the old key material")
	}
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("regenerate kept the old fingerprint")
	}
	got, ok := s.Get()
	if !ok || got != second {
		t.Fatal("regenerate did not persist the new pair")
	}
}

func dirOf(t *testing.T, st *state.Store) string {
	t.Helper()
	return strings.TrimSuffix(st.Path(), "/state.json")
}
