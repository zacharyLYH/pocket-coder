// Package sshkeys owns the server's git deploy keypair in the central
// state file (internal/state). Generated once on first boot via
// ssh-keygen (ed25519, no passphrase — non-interactive container use
// requires it); the public half goes to GitHub, the private half is
// injected into project containers. Regeneration replaces the pair, so
// the old public half must be removed everywhere it was installed.
package sshkeys

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"pcoder/internal/state"
)

// Key is the stored server deploy keypair.
type Key = state.ServerSSHKey

// Store manages the server keypair in the central state file.
type Store struct {
	st *state.Store
}

// New returns a key store backed by st.
func New(st *state.Store) *Store {
	return &Store{st: st}
}

// ErrNoKeygen is returned when ssh-keygen is unavailable.
var ErrNoKeygen = errors.New("ssh-keygen not found — install openssh-client")

// Get returns the stored keypair, or false when none exists yet.
func (s *Store) Get() (Key, bool) {
	var out Key
	var ok bool
	s.st.View(func(doc *state.Document) {
		if doc.ServerKey != nil {
			out = *doc.ServerKey
			ok = true
		}
	})
	return out, ok
}

// EnsureKeypair returns the stored keypair, generating and persisting one
// on first boot. Idempotent by construction: existing keys always win, so
// setup re-runs and restarts never rotate the key out from under GitHub.
func (s *Store) EnsureKeypair() (Key, error) {
	if k, ok := s.Get(); ok {
		return k, nil
	}
	return s.Regenerate()
}

// Regenerate replaces the keypair and returns the new one. Callers must
// surface the blast radius: the old public half must be removed everywhere
// it was installed (GitHub, DBs, other forges) or clones and pushes fail.
func (s *Store) Regenerate() (Key, error) {
	// The comment rides on the public key so it is recognizable in GitHub's
	// key list (e.g. "deploy key — me@example.com"). Fall back to "pcoder"
	// when state has not been seeded with an email yet.
	var email string
	s.st.View(func(doc *state.Document) {
		email = doc.User.Email
	})
	comment := email
	if comment == "" {
		comment = "pcoder"
	}
	priv, pub, fp, err := generate(comment)
	if err != nil {
		return Key{}, err
	}
	k := Key{
		PrivateKey:  priv,
		PublicKey:   pub,
		Fingerprint: fp,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.st.Mutate(func(doc *state.Document) error {
		doc.ServerKey = &k
		return nil
	}); err != nil {
		return Key{}, err
	}
	return k, nil
}

// generate shells out to ssh-keygen in a temp dir: ed25519, no passphrase.
// Temp files are removed before return; only the state file keeps the key.
func generate(comment string) (priv, pub, fp string, err error) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		return "", "", "", ErrNoKeygen
	}
	dir, err := os.MkdirTemp("", "pcoder-keygen")
	if err != nil {
		return "", "", "", err
	}
	defer os.RemoveAll(dir)
	base := filepath.Join(dir, "id_ed25519")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", comment, "-f", base, "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", "", "", fmt.Errorf("ssh-keygen: %s: %w", strings.TrimSpace(string(out)), err)
	}
	privRaw, err := os.ReadFile(base)
	if err != nil {
		return "", "", "", err
	}
	pubRaw, err := os.ReadFile(base + ".pub")
	if err != nil {
		return "", "", "", err
	}
	fpOut, err := exec.Command("ssh-keygen", "-lf", base+".pub", "-E", "sha256").Output()
	if err != nil {
		return "", "", "", fmt.Errorf("ssh-keygen -lf: %w", err)
	}
	// "256 SHA256:abc... (ED25519)" — the fingerprint is field two.
	fp = ""
	if fields := strings.Fields(string(fpOut)); len(fields) >= 2 {
		fp = fields[1]
	}
	if fp == "" {
		return "", "", "", errors.New("could not parse key fingerprint")
	}
	return string(privRaw), strings.TrimSpace(string(pubRaw)), fp, nil
}
