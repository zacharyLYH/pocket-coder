// Package harness manages harness plugins: CLI tools launchable as tmux
// sessions. The registry lives in the central state file (internal/state);
// builtins are seeded into it as real, editable entries, so builtins and
// user plugins share one code path.
package harness

import (
	"fmt"
	"sort"
	"strings"

	"pcoder/internal/state"
)

// Harness is a CLI plugin: a global entry in "+ New Session".
type Harness = state.Harness

// Binary is the executable the Command runs: its first word. Probing
// (installed checks, CLI validation) targets this; the rest of Command is
// arguments (e.g. "vi hello.txt" probes "vi").
func Binary(h Harness) string {
	return strings.Fields(h.Command)[0]
}

// Store reads and writes the plugin registry in the central state file.
type Store struct {
	st *state.Store
}

// New returns a plugin store backed by st.
func New(st *state.Store) *Store {
	return &Store{st: st}
}

// List returns every plugin, sorted by name, IDs synced from their map keys.
func (l *Store) List() ([]Harness, error) {
	out := []Harness{}
	l.st.View(func(doc *state.Document) {
		for id, h := range doc.Harnesses {
			h.ID = id
			out = append(out, h)
		}
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get returns one plugin by id.
func (l *Store) Get(id string) (Harness, error) {
	var (
		h  Harness
		ok bool
	)
	l.st.View(func(doc *state.Document) {
		h, ok = doc.Harnesses[id]
	})
	if !ok {
		return Harness{}, fmt.Errorf("no such harness %q", id)
	}
	h.ID = id
	return h, nil
}

// Save stores h under the slug of its name; an existing entry with that
// slug is refused (a name with no usable characters has no slug). It
// returns the id the plugin was saved under.
func (l *Store) Save(h Harness) (string, error) {
	id := Slug(h.Name)
	if id == "" {
		return "", fmt.Errorf("name %q has no usable characters for an id", h.Name)
	}
	if strings.TrimSpace(h.Command) == "" {
		return "", fmt.Errorf("name and command are required")
	}
	err := l.st.Mutate(func(doc *state.Document) error {
		if _, exists := doc.Harnesses[id]; exists {
			return fmt.Errorf("harness %q already exists", id)
		}
		h.ID = id
		doc.Harnesses[id] = h
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

// Remove deletes a harness from the registry. Deleting an unknown id is
// idempotent success (delete is idempotent everywhere else in the app).
func (l *Store) Remove(id string) error {
	return l.st.Mutate(func(doc *state.Document) error {
		delete(doc.Harnesses, id)
		return nil
	})
}

// EnsureBuiltins adds any missing builtin plugins to the registry, never
// overwriting existing entries (the user may have edited them). Returns the
// names written. Replaces the old seed-files-on-boot behavior: builtins are
// state now, seeded once and edited like any other plugin.
func (l *Store) EnsureBuiltins() ([]string, error) {
	var written []string
	err := l.st.Mutate(func(doc *state.Document) error {
		written = nil
		for _, b := range builtins {
			id := Slug(b.Name)
			if _, exists := doc.Harnesses[id]; exists {
				continue
			}
			b.ID = id
			doc.Harnesses[id] = b
			written = append(written, b.Name)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return written, nil
}

// Slug maps a harness name to a safe plugin/session id: lowercase, runs of
// anything outside [a-z0-9] collapsed to one dash, no leading/trailing dash.
func Slug(name string) string {
	var b strings.Builder
	lastDash := true // collapses runs and drops a leading dash
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

// Builtins seeded into a fresh registry. Real, editable entries;
// install/auth are best-effort defaults the user can change.
//
// Runtime requirements (visible failure with a hint comes free: the CLI
// validation probe reports the missing interpreter): node and python3 ship
// in the project image; the `pcoder-update-runtime` script inside every
// project brings them to the latest stable when run in a terminal.
//
// Where each records usage in its container's home volume (harvested by the
// observability page, read by the secretary later): opencode → its storage
// dir under ~/.local/share/opencode; cline → ~/.clinerules/ history; freebuff
// freebuff → its own state dir under ~. Terminal/bash records nothing.
// Credentials live in each CLI's native config file: users paste their
// config (configPath + config on the plugin, or edit it later) once.
//
// The two demo plugins exist so the platform's stories are visible from the
// "+ New Session" picker with zero setup: Vi Demo is a real full-screen TUI;
// Crasher Demo installs a CLI that validates fine, then exits 9 two seconds
// in — showing the `[crasher exited: 9]` failure line.
var builtins = []Harness{
	{Name: "Terminal", Command: "bash"},
	{Name: "OpenCode", Command: "opencode", Install: "npm i -g opencode-ai"},
	{Name: "Freebuff", Command: "freebuff", Install: "npm i -g freebuff && freebuff --version || true"},
	{Name: "Cline", Command: "cline", Install: "npm i -g cline"},
	{Name: "Vi Demo", Command: "vi notes.txt"},
	{Name: "Crasher Demo", Command: "crasher", Install: demoCrasherInstall},
}

// demoCrasherInstall writes the fake CLI the Crasher Demo harness runs.
const demoCrasherInstall = `printf '#!/bin/sh\nif [ $# -gt 0 ]; then echo "crasher 1.0"; exit 0; fi\necho "about to crash"\nsleep 2\nexit 9\n' > /usr/local/bin/crasher && chmod +x /usr/local/bin/crasher`
