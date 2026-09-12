// Package projectlog is the per-project running log: one in-memory,
// append-only ring per project that server code writes to instead of
// stdout. It is deliberately separate from both container logs and the
// global events.log — writers choose exactly what lands here as features
// grow, and readers get a scoped, per-project tail.
//
// Entries live for the process lifetime only; a restart starts empty.
// The Manager is the singleton: one per server, shared by all handlers.
package projectlog

import (
	"sync"
	"time"
)

// DefaultMaxEntries bounds memory per project. Oldest entries drop first;
// logging must never fail, so Append has no error return.
const DefaultMaxEntries = 500

// Entry is one log line.
type Entry struct {
	ID      int64          `json:"id"`
	Time    time.Time      `json:"time"`
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

// Manager owns every project's ring. Safe for concurrent use. All methods
// are safe on a nil receiver (no-op / empty), so handlers never nil-guard.
type Manager struct {
	mu      sync.RWMutex
	max     int
	buffers map[string]*buffer
	now     func() time.Time
}

type buffer struct {
	next    int64
	entries []Entry
}

// NewManager returns the singleton with room for max entries per project
// (DefaultMaxEntries when max <= 0).
func NewManager(max int) *Manager {
	if max <= 0 {
		max = DefaultMaxEntries
	}
	return &Manager{max: max, buffers: map[string]*buffer{}, now: time.Now}
}

// Append records one line for projectID, dropping the oldest when the ring
// is full.
func (m *Manager) Append(projectID, typ, message string, data map[string]any) Entry {
	if m == nil {
		return Entry{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buffers[projectID]
	if b == nil {
		b = &buffer{}
		m.buffers[projectID] = b
	}
	b.next++
	e := Entry{ID: b.next, Time: m.now(), Type: typ, Message: message, Data: data}
	b.entries = append(b.entries, e)
	if len(b.entries) > m.max {
		b.entries = append([]Entry(nil), b.entries[len(b.entries)-m.max:]...)
	}
	return e
}

// Read returns entries with ID greater than after, oldest first, at most
// limit (all when limit <= 0). Projects are isolated: entries never leak
// across project IDs.
func (m *Manager) Read(projectID string, after int64, limit int) []Entry {
	if m == nil {
		return []Entry{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b := m.buffers[projectID]
	if b == nil {
		return []Entry{}
	}
	out := []Entry{}
	for _, e := range b.entries {
		if e.ID > after {
			out = append(out, e)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
