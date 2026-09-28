// Package threads persists agent chats (butler, codemap, future agents)
// as one folder per thread, under an optional scope prefix.
//
//	$DATA_DIR/butler/<threadID>/                 scope = ""
//	$DATA_DIR/codemaps/<escaped-project>/<id>/   scope = project id
//	  manifest.json
//	  N.json        (one user-side turn per file, N = 1, 2, ...)
//	  N.lineage.json
//	  approvals.json  (butler only, when the store has Approvals: true)
//
// The folder name IS the thread ID (opaque hex MintID). No prompt text or
// slug appears in any filename. The manifest holds thread-level fields only
// ({id, title, createdAt}), set once at create. Each N.json holds a
// user-side Turn: envelope fields (prompt/turnId/time/error) plus an opaque
// Payload the owning product defines. Lineage files are write-only debug
// output: written at Complete, read only by diagnostics, never for
// follow-up context or history.
package threads

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Turn is the turn envelope. Payload is the owning product's shape, opaque
// to the store: butler marshals {answer, steps}, codemap marshals
// {sections, tools}.
type Turn struct {
	TurnID      string          `json:"turnId"`
	Prompt      string          `json:"prompt"`
	Time        time.Time       `json:"time"`
	Error       *string         `json:"error"`
	SHA         string          `json:"sha,omitempty"`
	ProjectHint string          `json:"projectHint,omitempty"`
	Payload     json.RawMessage `json:"payload"`
}

// Thread is one chat, reassembled from manifest.json + sorted N.json.
type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Turns     []Turn    `json:"turns"`

	// Approvals is empty unless the store has Approvals enabled.
	Approvals []Approval `json:"approvals,omitempty"`
}

// Manifest is the thread-level record: exactly {id, title, createdAt}.
type Manifest struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
}

// Summary is the list-view row: metadata without the turn bodies.
type Summary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	TurnCount int       `json:"turnCount"`
	Preview   string    `json:"preview"`
}

// Approval is one pending write (butler). Stored in approvals.json.
type Approval struct {
	ID          string          `json:"id"`
	TurnID      string          `json:"turnId,omitempty"`
	Status      string          `json:"status"`
	Tool        string          `json:"tool"`
	Args        json.RawMessage `json:"args"`
	Summary     string          `json:"summary"`
	BlastRadius string          `json:"blastRadius"`
	CreatedAt   time.Time       `json:"createdAt"`
	ResolvedAt  time.Time       `json:"resolvedAt,omitempty"`
}

// Approval statuses.
const (
	ApprovalPending   = "pending"
	ApprovalApproved  = "approved"
	ApprovalDiscarded = "discarded"
)

// ErrPendingApproval is returned by Reserve when a thread holds a pending
// approval: the user must confirm or discard before continuing.
var ErrPendingApproval = errors.New("pending confirmation")

// Store owns the thread folders. Safe for concurrent use. One global
// mutex (process-wide serialization; single-user app, harmless).
type Store struct {
	mu        sync.Mutex
	dir       string
	approvals bool
}

// New returns a store rooted at dir (e.g. $DATA_DIR/butler). With
// approvals true, the store maintains the approvals.json sidecar and
// reserve blocks on pending approvals.
func New(dir string, approvals bool) *Store {
	return &Store{dir: dir, approvals: approvals}
}

// MintID mints an opaque hex id.
func MintID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Hex fallback so the result still passes validID (hex-only).
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) scopeDir(scope string) string {
	if scope == "" {
		return s.dir
	}
	return filepath.Join(s.dir, urlPathEscape(scope))
}

func (s *Store) threadDir(scope, id string) string {
	return filepath.Join(s.scopeDir(scope), id)
}

// check rejects a missing store or blank scope before any thread I/O.
// Empty scope (butler) is allowed; the caller passes "" explicitly.
func (s *Store) check(scope string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("thread store not configured")
	}
	return nil
}

// urlPathEscape escapes one path segment (project ids may contain "/").
func urlPathEscape(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0f])
	}
	return b.String()
}

// validID is hex-only, ≤128 chars: MintID output passes, traversal blocked.
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
func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return string(r)
}

// TitleFromPrompt derives a chat title from the first prompt: first line,
// whitespace-collapsed, capped at 60 chars.
func TitleFromPrompt(prompt string) string {
	t := strings.TrimSpace(prompt)
	if i := strings.IndexAny(t, "\r\n"); i >= 0 {
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

// maxTurnN returns the highest N present.
func maxTurnN(threadDir string) int {
	ns := sortedTurnNs(threadDir)
	if len(ns) == 0 {
		return 0
	}
	return ns[len(ns)-1]
}

// sortedTurnNs lists N values in numeric file-N order. Gaps kept as-is.
func sortedTurnNs(threadDir string) []int {
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

// readTurn loads one N.json. A half-written file (JSON parse failure)
// reads as skipped, never fatal.
func readTurn(path string) (Turn, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Turn{}, false
	}
	var t Turn
	if err := json.Unmarshal(raw, &t); err != nil {
		return Turn{}, false
	}
	if t.TurnID == "" || t.Prompt == "" {
		return Turn{}, false
	}
	return t, true
}

// readManifest loads manifest.json.
func readManifest(threadDir string) (Manifest, bool) {
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

// writeJSONFile writes v as indented JSON, atomically (tmp + rename).
func writeJSONFile(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// persistTurn overwrites N.json and writes N.lineage.json beside it.
// Callers hold s.mu and verified the manifest.
func persistTurn(dir string, n int, turn Turn, lineage []byte) error {
	if turn.Time.IsZero() {
		turn.Time = time.Now().UTC()
	}
	if err := writeJSONFile(filepath.Join(dir, strconv.Itoa(n)+".json"), turn); err != nil {
		return err
	}
	if lineage != nil {
		return os.WriteFile(filepath.Join(dir, strconv.Itoa(n)+".lineage.json"), append(lineage, '\n'), 0o600)
	}
	return nil
}

// ReserveNewThread creates folder + manifest + 1.json placeholder atomically
// under one store lock. On failure the folder is removed and no threadId is
// returned. Title derives from the prompt. SHA and the opaque payload ride
// the placeholder when non-nil.
func (s *Store) ReserveNewThread(scope, prompt, sha string, payload json.RawMessage) (threadID, turnID string, err error) {
	if err := s.check(scope); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(prompt) == "" {
		return "", "", fmt.Errorf("prompt required")
	}
	threadID, turnID = MintID(), MintID()
	now := time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(scope, threadID)
	if err := writeJSONFile(filepath.Join(dir, "manifest.json"),
		Manifest{ID: threadID, Title: TitleFromPrompt(prompt), CreatedAt: now}); err != nil {
		return "", "", err
	}
	if err := writeJSONFile(filepath.Join(dir, "1.json"),
		Turn{TurnID: turnID, SHA: sha, Prompt: prompt, Payload: payload, Time: now}); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return threadID, turnID, nil
}

// ReserveFollowup appends the next N.json placeholder (max N + 1). When the
// store has approvals enabled, a pending approval blocks reservation.
func (s *Store) ReserveFollowup(scope, threadID, prompt, sha string, payload json.RawMessage) (int, string, error) {
	if err := s.check(scope); err != nil {
		return 0, "", err
	}
	if !validID(threadID) {
		return 0, "", fmt.Errorf("unknown thread")
	}
	if strings.TrimSpace(prompt) == "" {
		return 0, "", fmt.Errorf("prompt required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(scope, threadID)
	if _, ok := readManifest(dir); !ok {
		return 0, "", fmt.Errorf("unknown thread")
	}
	if s.approvals {
		for _, a := range readApprovals(dir) {
			if approvalStatus(a) == ApprovalPending {
				return 0, "", ErrPendingApproval
			}
		}
	}
	ns := sortedTurnNs(dir)
	if len(ns) == 0 {
		return 0, "", fmt.Errorf("unknown thread")
	}
	n := ns[len(ns)-1] + 1
	turnID := MintID()
	if err := writeJSONFile(filepath.Join(dir, strconv.Itoa(n)+".json"),
		Turn{TurnID: turnID, SHA: sha, Prompt: prompt, Payload: payload, Time: time.Now().UTC()}); err != nil {
		return 0, "", err
	}
	return n, turnID, nil
}

// CompleteTurn overwrites N.json with the finished turn and writes
// N.lineage.json beside it. A turn with Error set persists as a failed
// placeholder (visible, retryable); the caller fills Error itself.
func (s *Store) CompleteTurn(scope, threadID string, n int, turn Turn, lineage []byte) error {
	if err := s.check(scope); err != nil {
		return err
	}
	if !validID(threadID) || n < 1 || turn.TurnID == "" || strings.TrimSpace(turn.Prompt) == "" {
		return fmt.Errorf("bad turn")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(scope, threadID)
	if _, ok := readManifest(dir); !ok {
		return fmt.Errorf("unknown thread")
	}
	found := false
	for _, x := range sortedTurnNs(dir) {
		if x == n {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown turn")
	}
	return persistTurn(dir, n, turn, lineage)
}

// BeginRetry rewrites the LAST (highest-N) turn from scratch (same
// turnId/prompt, fresh time+sha, cleared payload and error, lineage
// dropped) and returns its N, turnId, and prompt. Context for the rerun is
// turns 1..N-1. Only a failed/errored last turn is retryable.
func (s *Store) BeginRetry(scope, threadID, newSHA string) (n int, turnID, prompt string, err error) {
	if err := s.check(scope); err != nil {
		return 0, "", "", err
	}
	if !validID(threadID) {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(scope, threadID)
	if _, ok := readManifest(dir); !ok {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	n = maxTurnN(dir)
	if n < 1 {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	cur, ok := readTurn(filepath.Join(dir, strconv.Itoa(n)+".json"))
	if !ok {
		return 0, "", "", fmt.Errorf("unknown thread")
	}
	if err := writeJSONFile(filepath.Join(dir, strconv.Itoa(n)+".json"),
		Turn{TurnID: cur.TurnID, SHA: newSHA, Prompt: cur.Prompt, Time: time.Now().UTC()}); err != nil {
		return 0, "", "", err
	}
	_ = os.Remove(filepath.Join(dir, strconv.Itoa(n)+".lineage.json"))
	return n, cur.TurnID, cur.Prompt, nil
}

// Get returns one thread with all its turns.
func (s *Store) Get(scope, id string) (Thread, error) {
	if err := s.check(scope); err != nil {
		return Thread{}, err
	}
	if !validID(id) {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(scope, id)
	man, ok := readManifest(dir)
	if !ok || man.ID != id {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	ns := sortedTurnNs(dir)
	if len(ns) == 0 {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	th := Thread{ID: man.ID, Title: man.Title, CreatedAt: man.CreatedAt, UpdatedAt: man.CreatedAt}
	for _, n := range ns {
		t, ok := readTurn(filepath.Join(dir, strconv.Itoa(n)+".json"))
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
	if s.approvals {
		th.Approvals = readApprovals(dir)
	}
	return th, nil
}

// List returns summaries sorted by createdAt, newest first.
func (s *Store) List(scope string) ([]Summary, error) {
	if err := s.check(scope); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.scopeDir(scope))
	if os.IsNotExist(err) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		dir := filepath.Join(s.scopeDir(scope), e.Name())
		man, ok := readManifest(dir)
		if !ok || man.ID != e.Name() {
			continue
		}
		first, ok := readTurn(filepath.Join(dir, "1.json"))
		if !ok {
			continue
		}
		ns := sortedTurnNs(dir)
		updated := man.CreatedAt
		if len(ns) > 0 {
			if last, ok := readTurn(filepath.Join(dir, strconv.Itoa(ns[len(ns)-1])+".json")); ok && !last.Time.IsZero() {
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

// Delete removes one thread folder. Idempotent.
func (s *Store) Delete(scope, id string) error {
	if err := s.check(scope); err != nil {
		return err
	}
	if !validID(id) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.threadDir(scope, id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DeleteScope removes every thread of one scope (a project's chats die with
// the project). Empty scope is refused: never wipe the whole store root. A
// missing directory is success.
func (s *Store) DeleteScope(scope string) error {
	if err := s.check(scope); err != nil {
		return err
	}
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("scope is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.scopeDir(scope)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadTurnLineage loads one N.lineage.json for diagnostics (no HTTP
// exposure, never read for follow-up context).
func (s *Store) ReadTurnLineage(scope, threadID string, n int) ([]byte, error) {
	if err := s.check(scope); err != nil {
		return nil, err
	}
	if !validID(threadID) || n < 1 {
		return nil, fmt.Errorf("unknown lineage")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(filepath.Join(s.threadDir(scope, threadID), strconv.Itoa(n)+".lineage.json"))
	if err != nil {
		return nil, fmt.Errorf("unknown lineage")
	}
	return raw, nil
}

// ── approvals sidecar (butler only; enabled via New) ──

func readApprovals(dir string) []Approval {
	raw, err := os.ReadFile(filepath.Join(dir, "approvals.json"))
	if err != nil {
		return nil
	}
	var out []Approval
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func approvalStatus(a Approval) string {
	if a.Status == "" {
		return ApprovalPending
	}
	return a.Status
}

// AddApproval appends one approval to approvals.json.
func (s *Store) AddApproval(threadID string, a Approval) error {
	if !s.approvals {
		return fmt.Errorf("approvals not enabled")
	}
	if !validID(threadID) || a.ID == "" || a.Tool == "" {
		return fmt.Errorf("bad approval")
	}
	if a.Status == "" {
		a.Status = ApprovalPending
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir("", threadID)
	if _, ok := readManifest(dir); !ok {
		return fmt.Errorf("unknown thread")
	}
	return writeJSONFile(filepath.Join(dir, "approvals.json"), append(readApprovals(dir), a))
}

// Approvals returns the thread's approvals.
func (s *Store) Approvals(threadID string) ([]Approval, error) {
	if !s.approvals {
		return nil, fmt.Errorf("approvals not enabled")
	}
	if !validID(threadID) {
		return nil, fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir("", threadID)
	if _, ok := readManifest(dir); !ok {
		return nil, fmt.Errorf("unknown thread")
	}
	return readApprovals(dir), nil
}

// ApprovalCount counts pending approvals across all threads.
func (s *Store) ApprovalCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _ := os.ReadDir(s.dir)
	n := 0
	for _, e := range entries {
		if e.IsDir() && validID(e.Name()) {
			for _, a := range readApprovals(s.threadDir("", e.Name())) {
				if approvalStatus(a) == ApprovalPending {
					n++
				}
			}
		}
	}
	return n
}

// FindApproval locates one approval by id, reporting its thread.
func (s *Store) FindApproval(id string) (Approval, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
	if err != nil && !os.IsNotExist(err) {
		return Approval{}, "", err
	}
	for _, e := range entries {
		if !e.IsDir() || !validID(e.Name()) {
			continue
		}
		for _, a := range readApprovals(s.threadDir("", e.Name())) {
			if a.ID == id {
				return a, e.Name(), nil
			}
		}
	}
	return Approval{}, "", fmt.Errorf("unknown approval")
}

// ResolveApproval sets one pending approval to approved/discarded.
func (s *Store) ResolveApproval(threadID, id, status string) error {
	if !s.approvals {
		return fmt.Errorf("approvals not enabled")
	}
	if !validID(threadID) {
		return fmt.Errorf("unknown thread")
	}
	if status != ApprovalApproved && status != ApprovalDiscarded {
		return fmt.Errorf("invalid approval status")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir("", threadID)
	items := readApprovals(dir)
	target := -1
	for i := range items {
		if items[i].ID == id {
			target = i
			break
		}
	}
	if target < 0 {
		return fmt.Errorf("unknown approval")
	}
	if approvalStatus(items[target]) != ApprovalPending {
		return fmt.Errorf("approval already resolved")
	}
	items[target].Status = status
	items[target].ResolvedAt = time.Now().UTC()
	return writeJSONFile(filepath.Join(dir, "approvals.json"), items)
}
