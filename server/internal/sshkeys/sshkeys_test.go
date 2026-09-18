package sshkeys

import (
	"testing"

	"pcoder/internal/state"
	"pcoder/internal/state/statetest"
)

func newState(t *testing.T) (*state.Store, *Store) {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st, New(st)
}

func TestAddAndList(t *testing.T) {
	st, s := newState(t)
	fp, err := s.Add("me@example.com", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", "laptop")
	if err != nil {
		t.Fatal(err)
	}
	if fp == "" {
		t.Fatal("empty fingerprint")
	}
	keys, err := s.List("me@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Fingerprint != fp {
		t.Fatalf("got %+v, want 1 key with fp %q", keys, fp)
	}
	if keys[0].Label != "laptop" {
		t.Fatalf("label = %q, want laptop", keys[0].Label)
	}
	if keys[0].PublicKey != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest" {
		t.Fatalf("publicKey = %q", keys[0].PublicKey)
	}
	if keys[0].Email != "me@example.com" {
		t.Fatalf("email = %q", keys[0].Email)
	}

	// on disk: exactly this key, exactly these fields (label set, no more)
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"sshKeys": []any{map[string]any{
			"fingerprint": "sha256-OTnE_vEFIEgKftAak-TpEw",
			"publicKey":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
			"label":       "laptop",
			"email":       "me@example.com",
		}},
	})
}

func TestAddDuplicateKeyRejected(t *testing.T) {
	st, s := newState(t)
	_, err := s.Add("me@example.com", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Add("me@example.com", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", "")
	if err == nil {
		t.Fatal("expected duplicate rejection")
	}
	// the rejected add must not have touched the file: still exactly one key
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"sshKeys": []any{map[string]any{
			"fingerprint": "sha256-OTnE_vEFIEgKftAak-TpEw",
			"publicKey":   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
			"email":       "me@example.com",
		}},
	})
}

func TestAddInvalidKeyRejected(t *testing.T) {
	st, s := newState(t)
	_, err := s.Add("me@example.com", "not-a-real-key", "")
	if err == nil {
		t.Fatal("expected invalid key rejection")
	}
	_, err = s.Add("me@example.com", "", "")
	if err == nil {
		t.Fatal("expected empty key rejection")
	}
	// neither rejection wrote anything
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
	})
}

func TestAddKeyFormats(t *testing.T) {
	cases := []struct {
		name, key, fingerprint string
	}{
		{"RSA", "ssh-rsa AAAAB3NzaC1yc2EAAAAITest user@host", "sha256-gQzYj37RsTCoxQWZy3cF3w"},
		{"ECDSA", "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAITest", "sha256-1wZmgswNte8_enve8t-vYQ"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, s := newState(t)
			fp, err := s.Add("me@example.com", tc.key, "")
			if err != nil {
				t.Fatal(err)
			}
			if fp == "" {
				t.Fatalf("empty fingerprint for %s key", tc.name)
			}
			statetest.AssertEqual(t, st.Path(), map[string]any{
				"user": map[string]any{"email": ""},
				"sshKeys": []any{map[string]any{
					"fingerprint": tc.fingerprint,
					"publicKey":   tc.key,
					"email":       "me@example.com",
				}},
			})
		})
	}
}

func TestDeleteKey(t *testing.T) {
	st, s := newState(t)
	fp, _ := s.Add("me@example.com", "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", "laptop")
	if err := s.Delete("me@example.com", fp); err != nil {
		t.Fatal(err)
	}
	keys, _ := s.List("me@example.com")
	if len(keys) != 0 {
		t.Fatalf("expected empty list after delete, got %+v", keys)
	}
	// the record is gone from the file entirely (no empty placeholder)
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
	})
	// deleting again is idempotent, as is deleting a never-existing key
	if err := s.Delete("me@example.com", fp); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("me@example.com", "sha256:nonexistent"); err != nil {
		t.Fatal(err)
	}
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
	})
}

func TestListEmptyIsArray(t *testing.T) {
	_, s := newState(t)
	keys, err := s.List("nobody@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if keys == nil {
		t.Fatal("nil instead of empty array")
	}
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys, got %d", len(keys))
	}
}

func TestAuthorizedKeys(t *testing.T) {
	st, s := newState(t)
	s.Add("me@example.com", "ssh-ed25519 AAAA-key1", "")
	s.Add("me@example.com", "ssh-ed25519 AAAA-key2", "")
	// exactly two records, sorted by fingerprint
	statetest.AssertEqual(t, st.Path(), map[string]any{
		"user": map[string]any{"email": ""},
		"sshKeys": []any{
			map[string]any{"fingerprint": "sha256-KDjYpE6jH3XJDD2RXMI2bw", "publicKey": "ssh-ed25519 AAAA-key1", "email": "me@example.com"},
			map[string]any{"fingerprint": "sha256-O4sPLkDlJqjlggDNQDt7jg", "publicKey": "ssh-ed25519 AAAA-key2", "email": "me@example.com"},
		},
	})
	ak, err := s.AllAuthorizedKeys()
	if err != nil {
		t.Fatal(err)
	}
	got := string(ak)
	if got != "ssh-ed25519 AAAA-key1\nssh-ed25519 AAAA-key2\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

// AllAuthorizedKeys ignores the owner: the injection set for containers is
// every registered key (single-user deployment).
func TestAllAuthorizedKeys(t *testing.T) {
	_, s := newState(t)
	s.Add("alice@example.com", "ssh-ed25519 AAAA-alice", "")
	s.Add("bob@example.com", "ssh-ed25519 AAAA-bob", "")
	ak, err := s.AllAuthorizedKeys()
	if err != nil {
		t.Fatal(err)
	}
	if string(ak) != "ssh-ed25519 AAAA-alice\nssh-ed25519 AAAA-bob\n" {
		t.Fatalf("all authorized_keys = %q", ak)
	}

	empty, err := New(mustEmptyState(t)).AllAuthorizedKeys()
	if err != nil || len(empty) != 0 {
		t.Fatalf("expected empty for no keys, got %q err=%v", empty, err)
	}
}

func mustEmptyState(t *testing.T) *state.Store {
	t.Helper()
	st, err := state.Open(t.TempDir(), state.Bootstrap{})
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestIsolationBetweenUsers(t *testing.T) {
	_, s := newState(t)
	s.Add("alice@example.com", "ssh-ed25519 AAAA-alice", "")
	s.Add("bob@example.com", "ssh-ed25519 AAAA-bob", "")

	alice, _ := s.List("alice@example.com")
	bob, _ := s.List("bob@example.com")
	if len(alice) != 1 || len(bob) != 1 {
		t.Fatalf("users not isolated: alice=%d bob=%d", len(alice), len(bob))
	}
	if alice[0].PublicKey == bob[0].PublicKey {
		t.Fatal("keys should differ between users")
	}
}

func TestFingerprintDeterministic(t *testing.T) {
	_, s := newState(t)
	fp1, _ := s.Add("a@test.com", "ssh-ed25519 AAAA-test", "")
	fp2, _ := s.Add("b@test.com", "ssh-ed25519 AAAA-test", "")
	// same key → same fingerprint (distinct registrations per owner)
	if fp1 != fp2 {
		t.Fatalf("same key produced different fingerprints: %q vs %q", fp1, fp2)
	}
}

func TestLooksLikeSSHPubKey(t *testing.T) {
	cases := map[string]bool{
		"ssh-ed25519 AAAA...":         true,
		"ssh-rsa AAAA...":             true,
		"ecdsa-sha2-nistp256 AAAA...": true,
		"not a key":                   false,
		"":                            false,
		" AAAA...":                    false,
	}
	for key, want := range cases {
		if got := looksLikeSSHPubKey(key); got != want {
			t.Errorf("looksLikeSSHPubKey(%q) = %v, want %v", key, got, want)
		}
	}
}
