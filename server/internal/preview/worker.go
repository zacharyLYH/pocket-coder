// Package preview contains the server-side control plane for remote browser
// previews. It deliberately does not know how Chromium is launched or how
// pixels are transported; those details belong behind WorkerFactory.
package preview

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var (
	ErrNotFound = errors.New("preview worker not found")
	ErrExists   = errors.New("preview worker already exists")
	ErrClosed   = errors.New("preview manager is closed")
)

// Config is the immutable identity/configuration of one project's browser.
type Config struct {
	ProjectID   string
	ContainerID string
}

// Endpoint describes a private worker endpoint. These values are server-side
// addresses only; handlers must never serialize them to an untrusted client.
type Endpoint struct {
	CDP     string
	Display string
}

// Worker is the runtime adapter implemented by the Chromium/display layer.
// Close must be safe to call more than once.
type Worker interface {
	Endpoint() Endpoint
	Close(ctx context.Context) error
}

// WorkerFactory starts the runtime for one project. The factory owns all
// process/container details, which keeps this package deterministic in tests.
type WorkerFactory interface {
	Start(ctx context.Context, cfg Config) (Worker, error)
}

// Manager owns at most one browser worker per project. It is safe for HTTP
// handlers, reconnects, and project lifecycle events to call concurrently.
type Manager struct {
	mu       sync.RWMutex
	factory  WorkerFactory
	workers  map[string]Worker
	inflight map[string]*startCall
	closed   bool
}

// startCall deduplicates concurrent Ensure starts for the same project: the
// preview surface iframe and the viewport auto-fit POST arrive together on
// every fresh PreviewSurface mount, and without this the losing starter
// fails on the duplicate sidecar container name.
type startCall struct {
	done   chan struct{}
	worker Worker
	err    error
}

func NewManager(factory WorkerFactory) *Manager {
	if factory == nil {
		panic("preview: nil worker factory")
	}
	return &Manager{factory: factory, workers: make(map[string]Worker), inflight: make(map[string]*startCall)}
}

// Ensure returns the existing worker or starts exactly one worker for the
// project. Starting happens outside the lock so a slow Chromium launch does
// not block unrelated projects; concurrent starters for the SAME project
// share one start instead of racing on the sidecar container name.
func (m *Manager) Ensure(ctx context.Context, cfg Config) (Worker, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrClosed
	}
	if w := m.workers[cfg.ProjectID]; w != nil {
		m.mu.Unlock()
		return w, nil
	}
	if c := m.inflight[cfg.ProjectID]; c != nil {
		m.mu.Unlock()
		<-c.done
		return c.worker, c.err
	}
	c := &startCall{done: make(chan struct{})}
	m.inflight[cfg.ProjectID] = c
	m.mu.Unlock()

	w, err := m.factory.Start(ctx, cfg)
	if err != nil {
		err = fmt.Errorf("start preview worker for %s: %w", cfg.ProjectID, err)
	}
	if err == nil && w == nil {
		err = fmt.Errorf("start preview worker for %s: factory returned nil worker", cfg.ProjectID)
	}

	m.mu.Lock()
	delete(m.inflight, cfg.ProjectID)
	if err == nil && !m.closed {
		// No existing-worker check here: the inflight map guarantees a
		// single starter per project and the lock is held from delete to
		// insert, so workers[id] cannot appear while Start runs.
		m.workers[cfg.ProjectID] = w
	}
	if m.closed && err == nil {
		m.mu.Unlock()
		_ = w.Close(context.Background())
		c.worker, c.err = nil, ErrClosed
		close(c.done)
		return nil, ErrClosed
	}
	m.mu.Unlock()
	c.worker, c.err = w, err
	close(c.done)
	return w, err
}

func (m *Manager) Get(projectID string) (Worker, error) {
	if projectID == "" {
		return nil, ErrNotFound
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if w := m.workers[projectID]; w != nil {
		return w, nil
	}
	return nil, ErrNotFound
}

// Stop removes a worker from the manager before closing it. A reconnect can
// therefore start a fresh worker even if the old runtime takes time to exit.
func (m *Manager) Stop(ctx context.Context, projectID string) error {
	m.mu.Lock()
	w := m.workers[projectID]
	if w != nil {
		delete(m.workers, projectID)
	}
	m.mu.Unlock()
	if w == nil {
		return ErrNotFound
	}
	return w.Close(ctx)
}

// Close stops every worker and prevents new workers from starting. It is
// intended for server shutdown and is idempotent.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	workers := make([]Worker, 0, len(m.workers))
	for id, w := range m.workers {
		workers = append(workers, w)
		delete(m.workers, id)
	}
	m.mu.Unlock()

	var first error
	for _, w := range workers {
		if err := w.Close(ctx); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func validateConfig(cfg Config) error {
	if cfg.ProjectID == "" {
		return fmt.Errorf("project id is required")
	}
	if cfg.ContainerID == "" {
		return fmt.Errorf("project container is required")
	}
	return nil
}
