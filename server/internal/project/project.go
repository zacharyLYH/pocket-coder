// Package project is the project control plane: desired state lives in the
// central state.json (internal/state), Docker containers/volumes are live
// state derived from the project id and reconciled against it.
package project

import (
	"fmt"
	"os"
	"sort"

	"pcoder/internal/state"
)

// Project aliases the canonical persisted shape.
type Project = state.Project

// Entry is one row of the projects index. The id is the repo's
// owner/repo — it is also the display name. Harnesses carries the
// installed harness ids, sorted, for display counts and the install-gated
// UI; in-flight ("installing") states live in state.json but never surface
// here — the per-project probe reports those.
type Entry struct {
	ID        string   `json:"id"`
	Harnesses []string `json:"harnesses,omitempty"`
}

// Store is what consumers need: CRUD over projects plus the index. Defined
// as an interface so callers can be tested with a mock instead of real disk.
type Store interface {
	Create(id string, p Project) error
	Get(id string) (Project, error)
	Update(id string, p Project) error
	Delete(id string) error
	List() ([]Entry, error)
	RecordInstall(projectID, harnessID string) error
	RecordInstalling(projectID, harnessID string) error
	ClearInstalling(projectID, harnessID string) error
	ClearAllInstalling() error
	HarnessStates(projectID string) (map[string]state.HarnessStatus, error)
	RecordSession(projectID, name, harnessID string) error
	GetSession(projectID, name string) (state.Session, bool)
	RemoveSession(projectID, name string) error
}

// StateStore implements Store over the central state file.
type StateStore struct {
	st *state.Store
}

// Open returns a project store backed by st.
func Open(st *state.Store) *StateStore {
	return &StateStore{st: st}
}

// Create writes a new project record.
func (s *StateStore) Create(id string, p Project) error {
	return s.st.Mutate(func(doc *state.Document) error {
		doc.Projects[id] = p
		return nil
	})
}

// Get reads one project.
func (s *StateStore) Get(id string) (Project, error) {
	var (
		p  Project
		ok bool
	)
	s.st.View(func(doc *state.Document) {
		p, ok = doc.Projects[id]
	})
	if !ok {
		return Project{}, fmt.Errorf("project %s: %w", id, os.ErrNotExist)
	}
	return p, nil
}

// Update replaces a project's record.
func (s *StateStore) Update(id string, p Project) error {
	return s.st.Mutate(func(doc *state.Document) error {
		if _, ok := doc.Projects[id]; !ok {
			return fmt.Errorf("project %s: %w", id, os.ErrNotExist)
		}
		doc.Projects[id] = p
		return nil
	})
}

// Delete removes a project's record.
func (s *StateStore) Delete(id string) error {
	return s.st.Mutate(func(doc *state.Document) error {
		delete(doc.Projects, id)
		return nil
	})
}

// updateProject loads one project, applies fn, and writes it back. The
// three install-state writers share it instead of repeating the lookup.
func (s *StateStore) updateProject(projectID string, fn func(*Project)) error {
	return s.st.Mutate(func(doc *state.Document) error {
		p, ok := doc.Projects[projectID]
		if !ok {
			return fmt.Errorf("project %s: %w", projectID, os.ErrNotExist)
		}
		if p.Harnesses == nil {
			p.Harnesses = map[string]state.HarnessStatus{}
		}
		fn(&p)
		doc.Projects[projectID] = p
		return nil
	})
}

// RecordInstall records that harnessID is installed in projectID.
// Idempotent: overwrites any "installing" mark with "true".
func (s *StateStore) RecordInstall(projectID, harnessID string) error {
	return s.updateProject(projectID, func(p *Project) {
		p.Harnesses[harnessID] = state.HarnessInstalled
	})
}

// RecordInstalling marks harnessID as mid-install in projectID. An already
// installed ("true") entry is left alone — reinstalling a present binary
// must not flip it back to installing.
func (s *StateStore) RecordInstalling(projectID, harnessID string) error {
	return s.updateProject(projectID, func(p *Project) {
		if p.Harnesses[harnessID] != state.HarnessInstalled {
			p.Harnesses[harnessID] = state.HarnessInstalling
		}
	})
}

// ClearInstalling drops an "installing" mark for harnessID in projectID. A
// "true" entry is never touched, so clearing after a successful install
// (which already overwrote the mark) is a safe no-op. Idempotent.
func (s *StateStore) ClearInstalling(projectID, harnessID string) error {
	return s.updateProject(projectID, func(p *Project) {
		if p.Harnesses[harnessID] == state.HarnessInstalling {
			delete(p.Harnesses, harnessID)
		}
	})
}

// ClearAllInstalling drops every "installing" mark in every project. Boot
// calls this before serving: any mark surviving a restart belongs to an
// exec that died with the old process, so nothing is actually running.
func (s *StateStore) ClearAllInstalling() error {
	return s.st.Mutate(func(doc *state.Document) error {
		for id, p := range doc.Projects {
			changed := false
			for hid, st := range p.Harnesses {
				if st == state.HarnessInstalling {
					delete(p.Harnesses, hid)
					changed = true
				}
			}
			if changed {
				doc.Projects[id] = p
			}
		}
		return nil
	})
}

// HarnessStates returns the raw install-state map for one project (a copy;
// absent ids and any "false" value mean not installed).
func (s *StateStore) HarnessStates(projectID string) (map[string]state.HarnessStatus, error) {
	var out map[string]state.HarnessStatus
	var ok bool
	s.st.View(func(doc *state.Document) {
		var p state.Project
		p, ok = doc.Projects[projectID]
		if !ok {
			return
		}
		out = map[string]state.HarnessStatus{}
		for hid, st := range p.Harnesses {
			out[hid] = st
		}
	})
	if !ok {
		return nil, fmt.Errorf("project %s: %w", projectID, os.ErrNotExist)
	}
	return out, nil
}

// InstalledIDs returns the sorted ids recorded "true" (installed) for one
// project — the stable UI list. "installing" entries are excluded: they are
// not installed yet and the probe reports them separately.
func InstalledIDs(p Project) []string {
	var out []string
	for hid, st := range p.Harnesses {
		if st == state.HarnessInstalled {
			out = append(out, hid)
		}
	}
	sort.Strings(out)
	return out
}

// List returns every project as an entry, sorted by id. Ids are
// owner/repo and unique, so the order is stable across calls (the home
// page's project pickers must not shuffle under the user). Each entry's
// harness list holds the installed ids, sorted.
func (s *StateStore) List() ([]Entry, error) {
	out := []Entry{}
	s.st.View(func(doc *state.Document) {
		for id, p := range doc.Projects {
			out = append(out, Entry{ID: id, Harnesses: InstalledIDs(p)})
		}
	})
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// RecordSession saves session metadata (which harness it runs) in the
// project's state.json entry. Called when a harness session is created or
// relaunched so restart/re-entry can look it up by name.
func (s *StateStore) RecordSession(projectID, name, harnessID string) error {
	return s.st.Mutate(func(doc *state.Document) error {
		p, ok := doc.Projects[projectID]
		if !ok {
			return fmt.Errorf("project %s: %w", projectID, os.ErrNotExist)
		}
		if p.Sessions == nil {
			p.Sessions = map[string]state.Session{}
		}
		p.Sessions[name] = state.Session{Harness: harnessID}
		doc.Projects[projectID] = p
		return nil
	})
}

// GetSession returns the session metadata for the given name. ok is false
// when the session has no metadata in state.json.
func (s *StateStore) GetSession(projectID, name string) (state.Session, bool) {
	var (
		sess state.Session
		ok   bool
	)
	s.st.View(func(doc *state.Document) {
		p, exists := doc.Projects[projectID]
		if !exists {
			return
		}
		sess, ok = p.Sessions[name]
	})
	return sess, ok
}

// RemoveSession deletes session metadata from state.json. Idempotent:
// removing a nonexistent session is not an error.
func (s *StateStore) RemoveSession(projectID, name string) error {
	return s.st.Mutate(func(doc *state.Document) error {
		p, ok := doc.Projects[projectID]
		if !ok {
			return nil
		}
		delete(p.Sessions, name)
		doc.Projects[projectID] = p
		return nil
	})
}
