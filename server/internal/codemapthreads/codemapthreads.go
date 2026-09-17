// Package codemapthreads persists codemap chats as one folder per thread.
//
//	$DATA_DIR/codemaps/<escaped-project>/<threadID>/
//	  manifest.json
//	  1.json
//	  1.lineage.json
//	  2.json
//	  2.lineage.json
//
// The folder name IS the thread ID (opaque stable MintID hex). No prompt
// text or slug appears in any filename. The manifest holds thread-level
// fields only ({id, title, createdAt}); each N.json holds a pure user-side
// Turn (prompt + sections + tools + time/sha/error); each N.lineage.json
// holds the debug-side agent.Lineage for that turn.
//
// Lineage files are write-only debug output: written on Complete/Fail,
// read only by diagnostics, never for follow-up context or history.
package codemapthreads

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Turn is one prompt/answer pair. N.json is the user-facing side: prompt
// + sections + tools + time/sha/error only. No ExtractorOutput, no
// threadId/project header.
type Turn struct {
	TurnID   string          `json:"turnId"`
	SHA      string          `json:"sha,omitempty"`
	Prompt   string          `json:"prompt"`
	Sections json.RawMessage `json:"sections"`
	Tools    json.RawMessage `json:"tools"`
	Time     time.Time       `json:"time"`
	Error    *string         `json:"error"`
}

// Thread is one chat, reassembled from manifest.json + sorted N.json.
type Thread struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Turns     []Turn    `json:"turns"`
}

// Manifest is the thread-level record: exactly {id, title, createdAt}.
// No project (parent dir is the scope), no updatedAt/nextTurnIndex/
// turnCount/preview. Set once at create; never bumped on turns.
type Manifest struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// Summary is the list-view row: metadata without the turn bodies.
// UpdatedAt is derived from the last turn file's time (== createdAt when
// empty); CreatedAt == manifest.createdAt.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	TurnCount int       `json:"turnCount"`
	Preview   string    `json:"preview"`
}

// Store owns the thread folders. Safe for concurrent use. One global
// mutex (process-wide serialization; single-user app, harmless).
type Store struct {
	mu  sync.Mutex
	dir string
}

// New returns a store rooted at dir (e.g. $DATA_DIR/codemaps).
func New(dir string) *Store {
	return &Store{dir: dir}
}

func MintID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Hex fallback so the result still passes validID (hex-only).
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) projectDir(project string) string {
	return filepath.Join(s.dir, url.PathEscape(project))
}

func (s *Store) threadDir(project, id string) string {
	return filepath.Join(s.projectDir(project), id)
}

// check rejects a missing store or blank project scope before any
// thread I/O, so empty projects never resolve to the store root.
func (s *Store) check(project string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("codemap store not configured")
	}
	if strings.TrimSpace(project) == "" {
		return fmt.Errorf("project is required")
	}
	return nil
}

// validID is hex-only: MintID output passes, traversal blocked.
func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

// truncate caps s at max runes, marking the cut with an ellipsis.
// Rune-aware: byte slicing could split a multi-byte rune.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

// TitleFromPrompt derives a chat title from the first prompt: first line,
// capped at 60 chars.
func TitleFromPrompt(prompt string) string {
	t := strings.TrimSpace(prompt)
	if i := strings.IndexByte(t, '\n'); i >= 0 {
		t = t[:i]
	}
	t = strings.Join(strings.Fields(t), " ")
	if t == "" {
		return "New chat"
	}
	return truncate(t, 60)
}

// turnFileN parses "N.json" (not lineage, not manifest). ok=false otherwise.
// Canonical form only: no leading zeros ("01.json" would collide with
// "1.json" via Atoi and let maxTurnN+1 overwrite an existing turn).
func turnFileN(name string) (int, bool) {
	if !strings.HasSuffix(name, ".json") {
		return 0, false
	}
	if strings.HasSuffix(name, ".lineage.json") {
		return 0, false
	}
	if name == "manifest.json" {
		return 0, false
	}
	base := strings.TrimSuffix(name, ".json")
	if len(base) > 1 && strings.HasPrefix(base, "0") {
		return 0, false
	}
	n, err := strconv.Atoi(base)
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

// sweepLegacyFlatFilesLocked deletes legacy flat files (*.json /
// *.lineage.json directly under the project dir): one-time reset, no
// back-compat. Callers hold s.mu.
func (s *Store) sweepLegacyFlatFilesLocked(project string) {
	dir := s.projectDir(project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".json") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// maxTurnNLocked returns the highest N present. Callers hold s.mu.
func maxTurnNLocked(threadDir string) int {
	entries, err := os.ReadDir(threadDir)
	if err != nil {
		return 0
	}
	max := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := turnFileN(e.Name()); ok && n > max {
			max = n
		}
	}
	return max
}

// sortedTurnNsLocked lists N values in numeric file-N order. Gaps kept
// as-is. Callers hold s.mu.
func sortedTurnNsLocked(threadDir string) []int {
	entries, err := os.ReadDir(threadDir)
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if n, ok := turnFileN(e.Name()); ok {
			out = append(out, n)
		}
	}
	sort.Ints(out)
	return out
}

// readTurnLocked loads one N.json. A half-written file (JSON parse
// failure) reads as skipped, never fatal. Callers hold s.mu.
func readTurnLocked(path string) (Turn, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Turn{}, false
	}
	var t Turn
	if err := json.Unmarshal(raw, &t); err != nil {
		return Turn{}, false
	}
	return t, true
}

// readManifestLocked loads manifest.json. Callers hold s.mu.
func readManifestLocked(threadDir string) (Manifest, bool) {
	raw, err := os.ReadFile(filepath.Join(threadDir, "manifest.json"))
	if err != nil {
		return Manifest{}, false
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, false
	}
	if m.ID == "" || m.CreatedAt.IsZero() {
		return Manifest{}, false
	}
	return m, true
}

// ReserveNewThread creates folder + manifest + 1.json placeholder
// atomically under one store lock. On failure the folder is removed and
// no threadId is returned. Title derives from the prompt.
func (s *Store) ReserveNewThread(project, prompt, sha string) (threadID, turnID string, err error) {
	if err := s.check(project); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(prompt) == "" {
		return "", "", fmt.Errorf("prompt is required")
	}
	threadID = MintID()
	turnID = MintID()
	now := time.Now().UTC()
	title := TitleFromPrompt(prompt)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLegacyFlatFilesLocked(project)
	dir := s.threadDir(project, threadID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(dir)
		}
	}()
	man := Manifest{ID: threadID, Title: title, CreatedAt: now}
	raw, err := json.MarshalIndent(man, "", "  ")
	if err != nil {
		return "", "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(raw, '\n'), 0o600); err != nil {
		return "", "", err
	}
	ph := Turn{TurnID: turnID, SHA: sha, Prompt: prompt, Time: now}
	praw, err := json.MarshalIndent(ph, "", "  ")
	if err != nil {
		return "", "", err
	}
	// Direct WriteFile, no tmp+rename (torn reads accepted as negligible).
	if err := os.WriteFile(filepath.Join(dir, "1.json"), append(praw, '\n'), 0o600); err != nil {
		return "", "", err
	}
	// No lineage placeholder: lineage is written once at Complete/Fail.
	failed = false
	return threadID, turnID, nil
}

// ReserveFollowup reserves max(existing N)+1 for an existing thread.
// Returns the turn index N and the new turnId.
func (s *Store) ReserveFollowup(project, threadID, prompt, sha string) (int, string, error) {
	if err := s.check(project); err != nil {
		return 0, "", err
	}
	if !validID(threadID) {
		return 0, "", fmt.Errorf("unknown thread")
	}
	if strings.TrimSpace(prompt) == "" {
		return 0, "", fmt.Errorf("prompt is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLegacyFlatFilesLocked(project)
	dir := s.threadDir(project, threadID)
	man, ok := readManifestLocked(dir)
	if !ok || man.ID != threadID {
		return 0, "", fmt.Errorf("unknown thread")
	}
	n := maxTurnNLocked(dir) + 1
	turnID := MintID()
	ph := Turn{TurnID: turnID, SHA: sha, Prompt: prompt, Time: time.Now().UTC()}
	raw, err := json.MarshalIndent(ph, "", "  ")
	if err != nil {
		return 0, "", err
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".json"), append(raw, '\n'), 0o600); err != nil {
		return 0, "", err
	}
	return n, turnID, nil
}

// persistTurnLocked overwrites N.json and writes N.lineage.json beside
// it. Callers hold s.mu and verified the manifest.
func persistTurnLocked(dir string, n int, turn Turn, lineage []byte) error {
	if turn.Time.IsZero() {
		turn.Time = time.Now().UTC()
	}
	raw, err := json.MarshalIndent(turn, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if lineage != nil {
		if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".lineage.json"), append(lineage, '\n'), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// CompleteTurn overwrites N.json with the full turn and writes
// N.lineage.json beside it, in place. Manifest untouched.
func (s *Store) CompleteTurn(project, threadID string, n int, turn Turn, lineage []byte) error {
	if err := s.check(project); err != nil {
		return err
	}
	if !validID(threadID) || n < 1 {
		return fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(project, threadID)
	if man, ok := readManifestLocked(dir); !ok || man.ID != threadID {
		return fmt.Errorf("unknown thread")
	}
	return persistTurnLocked(dir, n, turn, lineage)
}

// FailTurn fills error in N.json and writes N.lineage.json with the
// failure graph. The placeholder stays visible.
func (s *Store) FailTurn(project, threadID string, n int, turn Turn, errMsg string, lineage []byte) error {
	if err := s.check(project); err != nil {
		return err
	}
	if !validID(threadID) || n < 1 {
		return fmt.Errorf("unknown thread")
	}
	e := errMsg
	turn.Error = &e
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(project, threadID)
	if man, ok := readManifestLocked(dir); !ok || man.ID != threadID {
		return fmt.Errorf("unknown thread")
	}
	return persistTurnLocked(dir, n, turn, lineage)
}

// BeginRetry rewrites the LAST (highest-N) turn from scratch (same
// turnId/path/prompt, fresh time+sha, empty lineage) and returns its
// turnId, prompt, and N. Context for the rerun is turns 1..N-1 only.
// Only a failed/crashed last turn is retryable; callers enforce the
// guard via Get (last turn must be answer-less or errored).
func (s *Store) BeginRetry(project, threadID, newSHA string) (n int, turnID, prompt string, err error) {
	if err := s.check(project); err != nil {
		return 0, "", "", err
	}
	if !validID(threadID) {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(project, threadID)
	if man, ok := readManifestLocked(dir); !ok || man.ID != threadID {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	n = maxTurnNLocked(dir)
	if n < 1 {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	cur, ok := readTurnLocked(filepath.Join(dir, strconv.Itoa(n)+".json"))
	if !ok {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	ph := Turn{TurnID: cur.TurnID, SHA: newSHA, Prompt: cur.Prompt, Time: time.Now().UTC()}
	raw, err := json.MarshalIndent(ph, "", "  ")
	if err != nil {
		return 0, "", "", err
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".json"), append(raw, '\n'), 0o600); err != nil {
		return 0, "", "", err
	}
	_ = os.Remove(filepath.Join(dir, strconv.Itoa(n)+".lineage.json"))
	return n, cur.TurnID, cur.Prompt, nil
}

// Get reassembles a Thread from manifest.json + sorted N.json (numeric
// sort by file-N; skips manifest + *.lineage.json; gaps as-is in file-N
// order). Folders without a valid manifest: unknown-thread (404).
// Manifest-only folders (no 1.json yet): not visible (404).
func (s *Store) Get(project, id string) (Thread, error) {
	if err := s.check(project); err != nil {
		return Thread{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLegacyFlatFilesLocked(project)
	if !validID(id) {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	dir := s.threadDir(project, id)
	man, ok := readManifestLocked(dir)
	if !ok || man.ID != id {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	ns := sortedTurnNsLocked(dir)
	if len(ns) == 0 {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	th := Thread{ID: man.ID, Project: project, Title: man.Title, CreatedAt: man.CreatedAt, UpdatedAt: man.CreatedAt}
	for _, n := range ns {
		t, ok := readTurnLocked(filepath.Join(dir, strconv.Itoa(n)+".json"))
		if !ok {
			continue
		}
		th.Turns = append(th.Turns, t)
	}
	if len(th.Turns) == 0 {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	th.UpdatedAt = th.Turns[len(th.Turns)-1].Time
	if th.UpdatedAt.IsZero() {
		th.UpdatedAt = th.CreatedAt
	}
	return th, nil
}

// List returns summaries sorted strictly by createdAt, newest first.
// Per thread: manifest (id/title/createdAt) + 1.json for preview (first
// prompt, truncate 120) + count of N.json for turnCount. Skips folders
// without valid manifest or 1.json.
func (s *Store) List(project string) ([]Summary, error) {
	if err := s.check(project); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLegacyFlatFilesLocked(project)
	entries, err := os.ReadDir(s.projectDir(project))
	if os.IsNotExist(err) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !validID(id) {
			continue
		}
		dir := filepath.Join(s.projectDir(project), id)
		man, ok := readManifestLocked(dir)
		if !ok || man.ID != id {
			continue
		}
		first, ok := readTurnLocked(filepath.Join(dir, "1.json"))
		if !ok {
			continue
		}
		ns := sortedTurnNsLocked(dir)
		updated := man.CreatedAt
		if len(ns) > 0 {
			if last, ok := readTurnLocked(filepath.Join(dir, strconv.Itoa(ns[len(ns)-1])+".json")); ok && !last.Time.IsZero() {
				updated = last.Time
			}
		}
		out = append(out, Summary{
			ID: man.ID, Title: man.Title, CreatedAt: man.CreatedAt,
			UpdatedAt: updated, TurnCount: len(ns), Preview: truncate(first.Prompt, 120),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID > out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// Delete removes one thread folder. Idempotent. validID stays.
func (s *Store) Delete(project, id string) error {
	if err := s.check(project); err != nil {
		return err
	}
	if !validID(id) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.threadDir(project, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteProjectDir removes every thread of one project. A missing
// directory is success.
func (s *Store) DeleteProjectDir(project string) error {
	if err := s.check(project); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.projectDir(project)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadTurnLineage loads one N.lineage.json for diagnostics (no HTTP
// exposure). Not used by thread endpoints or follow-up context.
func (s *Store) ReadTurnLineage(project, threadID string, n int) ([]byte, error) {
	if err := s.check(project); err != nil {
		return nil, err
	}
	if !validID(threadID) || n < 1 {
		return nil, fmt.Errorf("unknown lineage")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.threadDir(project, threadID), strconv.Itoa(n)+".lineage.json"))
	if err != nil {
		return nil, fmt.Errorf("unknown lineage")
	}
	return raw, nil
}
