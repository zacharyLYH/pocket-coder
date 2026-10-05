// Package state owns state.json — the single source of truth for all
// desired app state: identity, SMTP credentials, projects, harness
// plugins, and the server SSH deploy keypair. Live state (Docker
// containers, tmux sessions) stays in Docker/tmux and is reconciled
// against this file.
//
// One JSON file with owner-only permissions, rewritten atomically (temp
// + rename) on every mutation. Small by construction — history lives in
// events.log, workspace bytes in docker volumes — so synchronous saves
// are fine.
package state

import (
	"crypto/rand"
	"encoding/hex"
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
	AIModels  []AIModel          `json:"ai_models,omitempty"`
	Projects  map[string]Project `json:"projects,omitempty"`  // keyed by project id
	Harnesses map[string]Harness `json:"harnesses,omitempty"` // keyed by harness slug id
	ServerKey *ServerSSHKey      `json:"serverKey,omitempty"`
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

// Shortcut is one user-defined terminal shortcut: a named value that is
// either injected into the session as a command or sent as raw key input,
// resolved from the value at run time.
type Shortcut struct {
	ID      string `json:"id"`
	Alias   string `json:"alias"` // label shown in the shortcuts modal
	Kind    string `json:"kind"`  // "cmd" or "keys" (run hint, resolved from the value)
	Command string `json:"command,omitempty"`
	Keys    string `json:"keys,omitempty"`
}

// DefaultShortcuts is the starter list for newly cloned projects: one
// command to install and start the app, plus the key sends a phone
// keyboard cannot type. Fresh copies only, callers must not mutate the
// result in place.
func DefaultShortcuts() []Shortcut {
	return []Shortcut{
		{ID: "default-esc", Alias: "Esc", Kind: "keys", Keys: "Esc"},
		{ID: "default-tab", Alias: "Tab", Kind: "keys", Keys: "Tab"},
		{ID: "default-up", Alias: "Up", Kind: "keys", Keys: "Up"},
		{ID: "default-down", Alias: "Down", Kind: "keys", Keys: "Down"},
		{ID: "default-left", Alias: "Left", Kind: "keys", Keys: "Left"},
		{ID: "default-right", Alias: "Right", Kind: "keys", Keys: "Right"},
		{ID: "default-ctrl-c", Alias: "Ctrl-C", Kind: "keys", Keys: "Ctrl-C"},
		{ID: "default-ctrl-d", Alias: "Ctrl-D", Kind: "keys", Keys: "Ctrl-D"},
	}
}

// Project is one project. Only what cannot be defaulted; the id is the
// repo's owner/repo (the display name), and the container/volumes are
// derived from it.
type Project struct {
	Repo      string                   `json:"repo"`
	Branch    string                   `json:"branch,omitempty"`
	Harnesses map[string]HarnessStatus `json:"harnesses,omitempty"` // install state by harness id
	Sessions  map[string]Session       `json:"sessions,omitempty"`  // keyed by session name
	Shortcuts []Shortcut               `json:"shortcuts,omitempty"` // the one shortcuts list (commands and keys alike)
}

// HarnessStatus is one harness's install state inside a project. Stored as
// a plain string: "true" (installed), "installing" (an explicit install is
// running on the server right now), or absent/"false" (not installed —
// "false" is never written, absence means it).
type HarnessStatus string

const (
	HarnessInstalled    HarnessStatus = "true"
	HarnessInstalling   HarnessStatus = "installing"
	HarnessNotInstalled HarnessStatus = "false"
)

// Session is high-level metadata about a tmux session. Stored in
// state.json so restart/re-entry can look up which harness a session
// was launched with, regardless of the user-given name.
type Session struct {
	Harness string `json:"harness,omitempty"` // harness ID, empty for plain shell
}

// Harness is a CLI plugin: a global entry in "+ New Session". The map key
// is the slug id; ID is kept in sync on load/save.
type Harness struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Command string `json:"command"`
	Install string `json:"install,omitempty"`
	// ConfigPath + Config are the CLI's OWN native configuration, verbatim:
	// at launch the platform writes Config into the container at exactly
	// ConfigPath. API keys live inside it because that is how those tools
	// take keys.
	ConfigPath string          `json:"configPath,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
}

// AIModel is one entry in the shared model list. API keys never leave
// the server in full: list responses carry hasKey only.
type AIModel struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	BaseURL string `json:"baseURL"`
	APIKey  string `json:"apiKey"`
	Model   string `json:"model"`
}

// MintID mints a hex id for list entries.
func MintID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}

// FirstAIModel returns the head of the shared model list, or nil when empty.
func FirstAIModel(doc *Document) *AIModel {
	if len(doc.AIModels) > 0 {
		return &doc.AIModels[0]
	}
	return nil
}

// ServerSSHKey is the server's own git deploy keypair, generated once on
// first boot. The public half goes to GitHub; the private half is injected
// into project containers for git-over-SSH. Fingerprint is the standard
// OpenSSH SHA256 form, for display only.
type ServerSSHKey struct {
	PrivateKey  string `json:"privateKey"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	CreatedAt   string `json:"createdAt"` // RFC3339
}

// Bootstrap seeds a fresh/empty document from environment-derived config.
// Existing values always win: env is only the initial default.
type Bootstrap struct {
	LoginEmail string
	SMTP       *SMTP
	AI         *AIModel
}

// Store is the in-memory handle over state.json. All reads go through
// View, all writes through Mutate (which persists atomically).
type Store struct {
	mu   sync.RWMutex
	path string
	doc  Document
}

// Open loads state.json from dataDir, creating an empty one when absent
// (seed values fill empty fields only — existing values always win).
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
// succeeds the copy is persisted and only then becomes current. A failed
// fn — or a failed persist — changes nothing on disk or in memory, so a
// 500 never leaves the two disagreeing about what exists.
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
	if err := s.saveDoc(clone); err != nil {
		return err
	}
	s.doc = clone
	return nil
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

// deepcopy round-trips through JSON: the document is small and plain,
// so this is impossible to get subtly wrong.
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
	return s.saveDoc(s.doc)
}

func (s *Store) saveDoc(doc Document) error {
	raw, err := json.MarshalIndent(doc, "", "  ")
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
	if b.AI != nil && len(s.doc.AIModels) == 0 {
		s.doc.AIModels = []AIModel{*b.AI}
	}
}
