// Package sshkeys stores registered SSH public keys in the central state
// file (internal/state). Users paste their public key once; the platform
// injects it into every sandbox container so git SSH clones work.
package sshkeys

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
	"strings"

	"sps/internal/state"
)

var (
	// ErrInvalidKey is returned for a public key that is empty or does not
	// look like an SSH public key.
	ErrInvalidKey = errors.New("not a valid SSH public key (expected ssh-ed25519, ssh-rsa, or ecdsa-sha2-* prefix)")
	// ErrDuplicateKey is returned when the exact key content is already
	// registered.
	ErrDuplicateKey = errors.New("key already registered")
)

// Key is a registered SSH public key.
type Key = state.SSHKey

// Store manages SSH public keys in the central state file.
type Store struct {
	st *state.Store
}

// New returns a key store backed by st.
func New(st *state.Store) *Store {
	return &Store{st: st}
}

// List returns every key for email, sorted by fingerprint.
func (s *Store) List(email string) ([]Key, error) {
	out := []Key{}
	s.st.View(func(doc *state.Document) {
		for _, k := range doc.SSHKeys {
			if k.Email == email {
				out = append(out, k)
			}
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Fingerprint < out[j].Fingerprint })
	return out, nil
}

// Add registers a public key for email and returns the derived fingerprint.
// The key content is validated minimally (must look like an SSH public key).
func (s *Store) Add(email, publicKey, label string) (string, error) {
	publicKey = strings.TrimSpace(publicKey)
	if publicKey == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidKey)
	}
	if !looksLikeSSHPubKey(publicKey) {
		return "", ErrInvalidKey
	}
	fp := fingerprint(publicKey)
	err := s.st.Mutate(func(doc *state.Document) error {
		for _, k := range doc.SSHKeys {
			if k.Fingerprint == fp && k.Email == email {
				return ErrDuplicateKey
			}
		}
		doc.SSHKeys = append(doc.SSHKeys, Key{
			Fingerprint: fp,
			PublicKey:   publicKey,
			Label:       label,
			Email:       email,
		})
		return nil
	})
	if err != nil {
		return "", err
	}
	return fp, nil
}

// Delete removes a key by fingerprint. Idempotent: deleting a missing key
// is not an error.
func (s *Store) Delete(email, fingerprint string) error {
	return s.st.Mutate(func(doc *state.Document) error {
		kept := doc.SSHKeys[:0]
		for _, k := range doc.SSHKeys {
			if k.Email == email && k.Fingerprint == fingerprint {
				continue
			}
			kept = append(kept, k)
		}
		doc.SSHKeys = kept
		return nil
	})
}

// AllAuthorizedKeys returns every registered key regardless of owner — the
// injection set for containers (single-user deployment: one key set).
func (s *Store) AllAuthorizedKeys() ([]byte, error) {
	var b strings.Builder
	s.st.View(func(doc *state.Document) {
		for _, k := range doc.SSHKeys {
			b.WriteString(k.PublicKey)
			b.WriteByte('\n')
		}
	})
	return []byte(b.String()), nil
}

// looksLikeSSHPubKey does a minimal prefix check to reject garbage.
func looksLikeSSHPubKey(key string) bool {
	for _, prefix := range []string{"ssh-ed25519 ", "ssh-rsa ", "ecdsa-sha2-"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// fingerprint derives a short identifier from the key content: sha256-<b64>.
// Uses a dash instead of colon so it is safe as an identifier.
func fingerprint(pubKey string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(pubKey)))
	// take first 16 bytes of the hash for a short but collision-resistant fp
	b64 := base64.RawURLEncoding.EncodeToString(h[:16])
	return "sha256-" + b64
}
