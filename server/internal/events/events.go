// Package events implements the server's audit log: an append-only JSONL
// file ($DATA_DIR/events.log) with a typed writer and a paginated read API
// (GET /api/events). History lives here, desired state in state.json —
// never confuse the two.
package events

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Event is one line in the log. ID is a per-log monotonic counter; the read
// API paginates with after=<last seen id>.
type Event struct {
	ID   int64          `json:"id"`
	Time time.Time      `json:"time"`
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Reader is the read side of the log, consumed by the HTTP layer.
type Reader interface {
	Read(after int64, limit int) ([]Event, error)
}

// Log appends events to a JSONL file. Safe for concurrent use.
type Log struct {
	mu   sync.Mutex
	path string
	file *os.File
	id   int64 // last written id
}

// Open opens the log for appending, creating it if needed. The next id
// continues from the last line so ids stay monotonic across restarts.
func Open(path string) (*Log, error) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	l := &Log{path: path, file: f}
	last, err := l.lastID()
	if err != nil {
		f.Close()
		return nil, err
	}
	l.id = last
	return l, nil
}

// Close closes the underlying file.
func (l *Log) Close() error {
	return l.file.Close()
}

// Append writes one event line and returns it with its assigned id.
func (l *Log) Append(typ string, data map[string]any) (Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.id++
	e := Event{ID: l.id, Time: time.Now().UTC(), Type: typ, Data: data}
	line, err := json.Marshal(e)
	if err != nil {
		return Event{}, fmt.Errorf("encode event: %w", err)
	}
	if _, err := l.file.Write(append(line, '\n')); err != nil {
		return Event{}, fmt.Errorf("append event: %w", err)
	}
	return e, nil
}

// Read returns events with ID greater than after, in file order, at most
// limit of them (limit <= 0 means no limit).
func (l *Log) Read(after int64, limit int) ([]Event, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return nil, fmt.Errorf("read event log: %w", err)
	}
	var out []Event
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // skip a torn/corrupt line, never fail the whole read
		}
		if e.ID <= after {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out, nil
}

// lastID scans the log for the highest event id.
func (l *Log) lastID() (int64, error) {
	evs, err := l.Read(0, 0)
	if err != nil {
		return 0, err
	}
	if len(evs) == 0 {
		return 0, nil
	}
	return evs[len(evs)-1].ID, nil
}
