package sync

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Code lifetime and guess budget. The spec fixes both: ten minutes and ten
// failed attempts before the code dies.
const (
	CodeTTL     = 10 * time.Minute
	MaxAttempts = 10
)

// crockford is Crockford base32: no I, L, O, or U, so codes cannot be
// misread or spell unfortunate words.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Direction is the sync direction chosen by the laptop: Upload moves the
// laptop's tree to pcoder, Download moves pcoder's tree to the laptop.
type Direction string

const (
	Upload   Direction = "upload"   // laptop → pcoder
	Download Direction = "download" // pcoder → laptop
)

// ValidDirection reports whether d is a direction the server knows.
func ValidDirection(d Direction) bool { return d == Upload || d == Download }

// ProjectStatus reports "idle", "uploading", or "downloading" for a project.
// The web UI blocks interaction while a transfer is live.
func (s *CodeStore) ProjectStatus(project string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sess := range s.sessions {
		if sess.Project != project || sess.finished.Load() || s.now().After(sess.ExpiresAt) {
			continue
		}
		if p, ok := sess.phase.Load().(string); ok {
			return p
		}
	}
	return "idle"
}

// Session is one live Work Sync. Server restart drops every session, which
// aborts an active sync — the user simply runs it again.
type Session struct {
	ID        string    `json:"session"`
	Project   string    `json:"project"`
	Nonce     string    `json:"nonce"`
	Branch    string    `json:"branch"`
	Commit    string    `json:"commit"`
	ExpiresAt time.Time `json:"expiresAt"`

	mu        sync.Mutex
	phase     atomic.Value // "uploading" | "downloading"
	codeHash  string
	code      string // plaintext, alive for the session's lifetime only
	container string
	dir       string
	direction Direction
	client    Manifest
	server    Manifest
	ops       []Op
	accepted  bool
	finished  atomic.Bool
	backup    string
}

// Container is the project container the session was opened against.
func (s *Session) Container() string { return s.container }

// codeEntry is one generated code, or — when unknown — the attempt tally for
// a code that was never generated. Only the hash is stored at rest; the
// plaintext lives in memory for the life of the session.
type codeEntry struct {
	hash      string
	plain     string
	project   string
	expires   time.Time
	attempts  int
	used      bool
	sessionID string
	unknown   bool
}

// CodeStore holds codes and live sessions. It is in-memory on purpose:
// server restart aborts active syncs (see the spec's invariants).
type CodeStore struct {
	mu        sync.Mutex
	entries   map[string]*codeEntry // by hash
	byProject map[string]string     // project → hash of its live code
	sessions  map[string]*Session
	now       func() time.Time
}

// NewCodeStore returns an empty store.
func NewCodeStore() *CodeStore {
	return &CodeStore{
		entries:   map[string]*codeEntry{},
		byProject: map[string]string{},
		sessions:  map[string]*Session{},
		now:       time.Now,
	}
}

// Generate creates a fresh 16-character code bound to project, invalidating
// any previous code for it. It refuses while a sync is actively running.
func (s *CodeStore) Generate(project string) (string, time.Time, error) {
	plain, err := newCode()
	if err != nil {
		return "", time.Time{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id := s.byProject[project]; id != "" {
		if old := s.entries[id]; old != nil && old.sessionID != "" {
			sess := s.sessions[old.sessionID]
			if sess != nil && !sess.finished.Load() && s.now().Before(sess.ExpiresAt) {
				return "", time.Time{}, SyncRunning()
			}
			// Finished or past its expiry: drop it so a laptop that
			// disconnected mid-sync cannot block this project forever.
			delete(s.sessions, old.sessionID)
		}
		delete(s.entries, s.byProject[project])
	}
	e := &codeEntry{hash: hashCode(plain), plain: plain, project: project, expires: s.now().Add(CodeTTL)}
	s.entries[e.hash] = e
	s.byProject[project] = e.hash
	return plain, e.expires, nil
}

// Lookup validates a hello attempt and returns the session bound to the
// code, creating it on first use. Repeating hello with the same code and
// project returns the same live session until expiry.
func (s *CodeStore) Lookup(code, project, container, dir, branch, commit string) (*Session, error) {
	hash := hashCode(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[hash]
	if e == nil {
		// Guessing a code that was never generated burns attempts too, so
		// guessing is never free.
		e = &codeEntry{hash: hash, unknown: true}
		s.entries[hash] = e
	}
	if e.unknown {
		return s.failAttempt(e, hash, CodeInvalid())
	}
	if s.now().After(e.expires) {
		return s.failAttempt(e, hash, CodeExpired())
	}
	if e.project != project {
		return s.failAttempt(e, hash, CodeWrongProject(e.project, project))
	}
	if e.sessionID != "" {
		if sess := s.sessions[e.sessionID]; sess != nil && !sess.finished.Load() {
			return sess, nil
		}
	}
	if e.used || e.plain == "" {
		// The plan was accepted, or the session ended: either way this code
		// cannot open another session.
		return nil, CodeUsed()
	}
	nonce, err := NewNonce()
	if err != nil {
		return nil, err
	}
	id, err := NewNonce()
	if err != nil {
		return nil, err
	}
	sess := &Session{
		ID: id, Project: project, Nonce: nonce, Branch: branch, Commit: commit,
		ExpiresAt: e.expires, codeHash: e.hash, code: e.plain, container: container, dir: dir,
	}
	s.sessions[id] = sess
	e.sessionID = id
	return sess, nil
}

// failAttempt spends one attempt on a failed lookup and kills the entry once
// the budget is gone: the spec allows ten failures per code.
func (s *CodeStore) failAttempt(e *codeEntry, hash string, err error) (*Session, error) {
	e.attempts++
	if e.attempts >= MaxAttempts {
		delete(s.entries, hash)
	}
	return nil, err
}

// Session returns the live session by id.
func (s *CodeStore) Session(id string) (*Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return nil, SessionUnknown()
	}
	if s.now().After(sess.ExpiresAt) {
		sess.finished.Store(true)
		return nil, SessionExpired()
	}
	return sess, nil
}

// Finish marks a session done and frees the project for the next sync.
func (s *CodeStore) Finish(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[id]
	if sess == nil {
		return
	}
	if e := s.entries[sess.codeHash]; e != nil {
		// The plaintext lives only as long as its session; the hash stays so
		// a used code is still recognised.
		e.plain = ""
	}
	if s.byProject[sess.Project] == sess.codeHash {
		delete(s.byProject, sess.Project)
	}
	delete(s.sessions, id)
}

// markUsed flips the code to used. Called when the transfer plan is
// accepted, which is the one-time-use boundary in the spec.
func (s *CodeStore) markUsed(sess *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.entries[sess.codeHash]; e != nil {
		e.used = true
	}
}

func hashCode(code string) string {
	sum := sha256.Sum256([]byte(NormalizeCode(code)))
	return hex.EncodeToString(sum[:])
}

// NormalizeCode canonicalizes a code for hashing and key derivation, so a
// laptop that types lowercase or ambiguous characters still derives the
// same AES key as the server.
func NormalizeCode(code string) string { return normalizeCode(code) }

// normalizeCode uppercases and strips separators, then applies Crockford's
// ambiguous-character mapping (I/L→1, O→0).
func normalizeCode(code string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ', '\t', '\n':
			continue
		case 'I', 'L':
			r = '1'
		case 'O':
			r = '0'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// newCode draws 16 Crockford characters (80 bits) and groups them
// XXXX-XXXX-XXXX-XXXX. Values above the alphabet are rejected, keeping the
// distribution uniform.
func newCode() (string, error) {
	out := make([]rune, 0, 16)
	buf := make([]byte, 1)
	for len(out) < 16 {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if int(buf[0]) >= 256-256%len(crockford) {
			continue
		}
		out = append(out, rune(crockford[int(buf[0])%len(crockford)]))
	}
	parts := make([]string, 4)
	for i := 0; i < 4; i++ {
		parts[i] = string(out[i*4 : i*4+4])
	}
	return strings.Join(parts, "-"), nil
}
