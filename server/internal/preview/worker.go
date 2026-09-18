// Package preview contains the server-side control plane for remote browser
// previews. It deliberately does not know how Chromium is launched or how
// pixels are transported; those details belong behind WorkerFactory.
package preview

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

var (
	ErrNotFound = errors.New("preview worker not found")
	ErrClosed   = errors.New("preview manager is closed")
)

// Config is the immutable identity/configuration of one project's browser.
type Config struct {
	ProjectID   string
	ContainerID string
	Port        int
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
	mu        sync.RWMutex
	factory   WorkerFactory
	workers   map[string]Worker
	inflight  map[string]*startCall
	closed    bool
	tokens    map[string]string
	lastSeen  map[string]time.Time
	sweepStop chan struct{}
	sweepOnce sync.Once
	now       func() time.Time
}

// startCall deduplicates concurrent Ensure starts for the same project:
// racing starters collide on the sidecar container name.
type startCall struct {
	done   chan struct{}
	worker Worker
	err    error
}

func NewManager(factory WorkerFactory) *Manager {
	if factory == nil {
		panic("preview: nil worker factory")
	}
	return &Manager{factory: factory, workers: make(map[string]Worker), inflight: make(map[string]*startCall), tokens: make(map[string]string), lastSeen: make(map[string]time.Time), now: time.Now}
}

// Ensure returns the existing worker or starts exactly one worker for the
// project. Starting happens outside the lock so a slow Chromium launch does
// not block unrelated projects; concurrent starters for the same project
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
		if _, dup := m.workers[cfg.ProjectID]; dup {
			m.mu.Unlock()
			_ = w.Close(context.Background())
			c.worker, c.err = m.workers[cfg.ProjectID], nil
			close(c.done)
			return c.worker, nil
		}
		// The inflight map guarantees a single starter per project and the
		// lock is held from delete to insert, so no existing worker can
		// appear while Start runs.
		m.workers[cfg.ProjectID] = w
		if _, ok := m.tokens[cfg.ProjectID]; !ok {
			m.tokens[cfg.ProjectID] = newToken()
			m.lastSeen[cfg.ProjectID] = m.clock()
			// The value itself is never logged: this line proves a
			// capability was issued, not what it is.
			slog.Info("preview token minted", "project", cfg.ProjectID, "reason", "start")
		}
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
		delete(m.tokens, projectID)
		delete(m.lastSeen, projectID)
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
	if m.sweepStop != nil {
		close(m.sweepStop)
		m.sweepStop = nil
	}
	workers := make([]Worker, 0, len(m.workers))
	for id, w := range m.workers {
		workers = append(workers, w)
		delete(m.workers, id)
		delete(m.tokens, id)
		delete(m.lastSeen, id)
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

// TokenOrMint returns the live token, minting one if the worker exists but
// has no token (post-sweep rotation). False when no worker is running.
func (m *Manager) TokenOrMint(projectID string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.workers[projectID]; !ok {
		return "", false
	}
	if tok, ok := m.tokens[projectID]; ok {
		return tok, true
	}
	tok := newToken()
	if m.tokens == nil {
		m.tokens = make(map[string]string)
	}
	if m.lastSeen == nil {
		m.lastSeen = make(map[string]time.Time)
	}
	m.tokens[projectID] = tok
	m.lastSeen[projectID] = m.clock()
	// Fresh mint while a worker is live means the previous token was
	// swept after total silence: this is the rotation successor.
	slog.Info("preview token minted", "project", projectID, "reason", "rotation")
	return tok, true
}

// CheckToken reports whether sup is the live token for projectID.
func (m *Manager) CheckToken(projectID, sup string) bool {
	m.mu.RLock()
	tok, ok := m.tokens[projectID]
	m.mu.RUnlock()
	if !ok || sup == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(sup)) == 1
}

// Touch marks projectID as recently seen by a valid token holder.
func (m *Manager) Touch(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tokens[projectID]; ok {
		m.lastSeen[projectID] = m.clock()
	}
}

// Sweep deletes tokens (and their presence) silent longer than silence.
func (m *Manager) Sweep(now time.Time, silence time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var swept []string
	for id, seen := range m.lastSeen {
		if now.Sub(seen) > silence {
			delete(m.tokens, id)
			delete(m.lastSeen, id)
			swept = append(swept, id)
		}
	}
	if len(swept) > 0 {
		slog.Info("preview tokens swept after silence", "projects", swept, "silence", silence.String())
	}
}

// StartSweeper runs one global ticker expiring silent tokens. Once-guarded;
// Close stops it first.
func (m *Manager) StartSweeper(interval, silence time.Duration) {
	m.sweepOnce.Do(func() {
		m.mu.Lock()
		if m.closed {
			m.mu.Unlock()
			return
		}
		stop := make(chan struct{})
		m.sweepStop = stop
		m.mu.Unlock()
		go func() {
			t := time.NewTicker(interval)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case now := <-t.C:
					m.Sweep(now, silence)
				}
			}
		}()
	})
}

func (m *Manager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func newToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("preview: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
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
