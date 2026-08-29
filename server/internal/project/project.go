// Package project is the project control plane: desired state lives in the
// central state.json (internal/state), Docker containers/volumes are live
// state derived from the project id and reconciled against it.
package project

import (
	"fmt"
	"os"
	"sort"

	"sps/internal/state"
)

// Project aliases the canonical persisted shape.
type Project = state.Project

// Entry is one row of the projects index.
type Entry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
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

// RecordInstall records that harnessID is installed in projectID.
// Idempotent: duplicate installs do not reorder or duplicate.
func (s *StateStore) RecordInstall(projectID, harnessID string) error {
	return s.st.Mutate(func(doc *state.Document) error {
		p, ok := doc.Projects[projectID]
		if !ok {
			return fmt.Errorf("project %s: %w", projectID, os.ErrNotExist)
		}
		for _, h := range p.Harnesses {
			if h == harnessID {
				return nil
			}
		}
		p.Harnesses = append(p.Harnesses, harnessID)
		doc.Projects[projectID] = p
		return nil
	})
}

// List returns every project as an entry, sorted by name then id. Name,
// then id: blank sandboxes are all "untitled", and an unstable tiebreak
// would reorder the list between API calls (the home page's project pickers
// must not shuffle under the user).
func (s *StateStore) List() ([]Entry, error) {
	out := []Entry{}
	s.st.View(func(doc *state.Document) {
		for id, p := range doc.Projects {
			h := p.Harnesses
			if h != nil {
				cp := make([]string, len(h))
				copy(cp, h)
				h = cp
			}
			out = append(out, Entry{ID: id, Name: p.Name, Harnesses: h})
		}
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
