// Package state owns state.json — the single source of truth for all
// desired app state: the user's identity, SMTP credentials, projects,
// harness plugins (with their native CLI configs), and SSH public keys.
//
// Everything here is DESIRED state ("what should exist"). Live state
// (Docker containers, tmux sessions) stays in Docker/tmux and is
// reconciled against this file (see project.Service.EnsureContainer).
//
// The whole document is one JSON file with owner-only permissions,
// rewritten atomically (temp + rename) on every mutation. It is small by
// construction — history lives in events.log, workspace bytes in docker
// volumes — so synchronous saves are fine.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Document is the entire persisted app state.
type Document struct {
	User      User               `json:"user"`
	SMTP      *SMTP              `json:"smtp,omitempty"`
	Projects  map[string]Project `json:"projects,omitempty"`  // keyed by project id
	Harnesses map[string]Harness `json:"harnesses,omitempty"` // keyed by harness slug id
	SSHKeys   []SSHKey           `json:"sshKeys,omitempty"`
}

// User is the single login identity.
type User struct {
	Email string `json:"email"`
}

// SMTP holds the mail-server credentials used to deliver login PINs.
type SMTP struct {
	Host     string `json:"host"`
	Port     int    `json:"port,omitempty"`
	User     string `json:"user"`
	Password string `json:"password"`
	From     string `json:"from,omitempty"`
}

// Project is one project. Only what cannot be defaulted; the id is the
// repo's owner/repo (the display name), and the container/volumes are
// derived from it and reconciled from here.
type Project struct {
	Repo          string             `json:"repo"`
	Branch        string             `json:"branch,omitempty"`
	CloneMethod   string             `json:"cloneMethod,omitempty"`   // "ssh" or "http" (default)
	Harnesses     []string           `json:"harnesses,omitempty"`     // installed harness ids, ordered by install
	Sessions      map[string]Session `json:"sessions,omitempty"`      // keyed by session name
	QuickCommands map[string]string  `json:"quickCommands,omitempty"` // alias → command
}

// Session is high-level metadata about a tmux session. Stored in
// state.json so restart/re-entry can look up which harness a session
// was launched with, regardless of the user-given name.
type Session struct {
	Harness string `json:"harness,omitempty"` // harness ID, empty for plain shell
}

// Harness is a CLI plugin: a global entry in "+ New Session". The map key
// is the slug id; ID is kept in sync on load/save and exists so API
// responses can carry it.
type Harness struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Install string `json:"install,omitempty"`
	// ConfigPath + Config are the CLI's OWN native configuration, verbatim:
	// at launch the platform writes Config into the container at exactly
	// ConfigPath. API keys live inside it because that is how those tools
	// take keys — so they live here too, in the one state file.
	ConfigPath string          `json:"configPath,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// SSHKey is a registered public key, injected into projects for
// git SSH clones. Fingerprint is derived from PublicKey content.
type SSHKey struct {
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"publicKey"`
	Label       string `json:"label,omitempty"`
	Email       string `json:"email"`
}

// Bootstrap seeds a fresh/empty document from environment-derived config.
// Existing values always win: env is only the initial default.
type Bootstrap struct {
	LoginEmail string
	SMTP       *SMTP
}

// Store is the in-memory handle over state.json. All reads go through
// View, all writes through Mutate (which persists atomically).
type Store struct {
	mu   sync.RWMutex
	path string
	doc  Document
}

// Open loads state.json from dataDir, creating an empty one when absent
// (seed values fill empty fields, so env config acts as the initial default
// only).
func Open(dataDir string, seed Bootstrap) (*Store, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dataDir, err)
	}
	s := &Store{path: filepath.Join(dataDir, "state.json")}
	raw, err := os.ReadFile(s.path)
	fresh := os.IsNotExist(err)
	switch {
	case err == nil:
		if err := json.Unmarshal(raw, &s.doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
	case fresh:
		// fresh install: empty document, seeded below and persisted
	default:
		return nil, fmt.Errorf("read %s: %w", s.path, err)
	}
	s.seed(seed)
	if !fresh { // loaded an existing doc — nothing to persist
		return s, nil
	}
	if err := s.save(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path is the absolute path of the backing file.
func (s *Store) Path() string { return s.path }

// View runs fn over the document under a read lock. fn must not retain
// references past its return (copy out what you need).
func (s *Store) View(fn func(doc *Document)) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	fn(&s.doc)
}

// Mutate runs fn over a copy of the document under a write lock; when fn
// succeeds the copy becomes current and is persisted atomically. A failed
// fn changes nothing on disk or in memory.
func (s *Store) Mutate(fn func(doc *Document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clone, err := deepcopy(s.doc)
	if err != nil {
		return err
	}
	normalize(&clone) // fn can assume the keyed collections are non-nil
	if err := fn(&clone); err != nil {
		return err
	}
	s.doc = clone
	return s.save()
}

// normalize guarantees the keyed collections are non-nil for mutation fns,
// so no caller of Mutate needs a nil-map guard before writing. Empty maps
// stay out of the file: omitempty drops them at save time either way.
func normalize(doc *Document) {
	if doc.Projects == nil {
		doc.Projects = map[string]Project{}
	}
	if doc.Harnesses == nil {
		doc.Harnesses = map[string]Harness{}
	}
}

// deepcopy round-trips through JSON: the document is small and plain, so
// this is cheap and impossible to get subtly wrong.
func deepcopy(doc Document) (Document, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return Document{}, err
	}
	var out Document
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("rename %s: %w", s.path, err)
	}
	return nil
}

func (s *Store) seed(b Bootstrap) {
	if b.LoginEmail != "" && s.doc.User.Email == "" {
		s.doc.User.Email = b.LoginEmail
	}
	if b.SMTP != nil && s.doc.SMTP == nil {
		s.doc.SMTP = b.SMTP
	}
}
