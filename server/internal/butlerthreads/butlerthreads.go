// Package butlerthreads persists butler chats as one folder per thread.
//
//	$DATA_DIR/butler/<threadID>/
//	  manifest.json
//	  1.json
//	  2.json
//
// One global history: no project scope (the current page passes only a
// hint per turn). Shape mirrors codemapthreads minus lineage: the folder
// name IS the thread ID (opaque hex MintID), the manifest holds {id,
// title, createdAt} only, each N.json holds one user-side Turn.
package butlerthreads

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Step is one tool start/finish pair shown in the collapsed "N steps" row.
type Step struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Turn is one prompt/answer pair with the tool steps behind it and the
// project hint it ran under.
type Turn struct {
	TurnID      string    `json:"turnId"`
	Prompt      string    `json:"prompt"`
	Answer      string    `json:"answer,omitempty"`
	Steps       []Step    `json:"steps,omitempty"`
	ProjectHint string    `json:"projectHint,omitempty"`
	Time        time.Time `json:"time"`
	Error       *string   `json:"error,omitempty"`
}

// Thread is one chat, reassembled from manifest.json + sorted N.json.
type Thread struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Turns     []Turn    `json:"turns"`
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

// Store owns the thread folders. Safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	dir string
}

// New returns a store rooted at dir (e.g. $DATA_DIR/butler).
func New(dir string) *Store {
	return &Store{dir: dir}
}

func MintID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func validID(id string) bool {
	if len(id) != 24 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (s *Store) threadDir(id string) string {
	return filepath.Join(s.dir, id)
}

func truncate(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n])
}

// TitleFromPrompt derives the thread title from the first prompt.
func TitleFromPrompt(prompt string) string {
	line := prompt
	if i := strings.IndexAny(line, "\r\n"); i >= 0 {
		line = line[:i]
	}
	title := strings.Join(strings.Fields(line), " ")
	if title == "" {
		return "New chat"
	}
	if r := []rune(title); len(r) > 60 {
		return string(r[:60]) + "…"
	}
	return title
}

func writeJSONFile(path string, v any) error {
	raw, err := json.Marshal(v)
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

func readManifestLocked(dir string) (Manifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Manifest{}, false
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		return Manifest{}, false
	}
	if man.ID == "" || man.CreatedAt.IsZero() {
		return Manifest{}, false
	}
	return man, true
}

func readTurnLocked(path string) (Turn, bool) {
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
	if t.Steps == nil {
		t.Steps = []Step{}
	}
	return t, true
}

func sortedTurnNsLocked(dir string) []int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var ns []int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".json")
		if base == "manifest" {
			continue
		}
		n, err := strconv.Atoi(base)
		if err != nil || n < 1 {
			continue
		}
		ns = append(ns, n)
	}
	sort.Ints(ns)
	return ns
}

// ReserveNewThread creates the folder, manifest, and 1.json placeholder.
func (s *Store) ReserveNewThread(prompt string, projectHint string) (string, string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", "", fmt.Errorf("prompt required")
	}
	threadID, turnID := MintID(), MintID()
	now := time.Now().UTC()
	dir := s.threadDir(threadID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeJSONFile(filepath.Join(dir, "manifest.json"),
		Manifest{ID: threadID, Title: TitleFromPrompt(prompt), CreatedAt: now}); err != nil {
		return "", "", err
	}
	if err := writeJSONFile(filepath.Join(dir, "1.json"),
		Turn{TurnID: turnID, Prompt: prompt, ProjectHint: projectHint, Time: now}); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", err
	}
	return threadID, turnID, nil
}

// ReserveFollowup appends the next N.json placeholder (max N + 1).
func (s *Store) ReserveFollowup(threadID string, prompt string, projectHint string) (int, string, error) {
	if !validID(threadID) {
		return 0, "", fmt.Errorf("unknown thread")
	}
	if strings.TrimSpace(prompt) == "" {
		return 0, "", fmt.Errorf("prompt required")
	}
	turnID := MintID()
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(threadID)
	if _, ok := readManifestLocked(dir); !ok {
		return 0, "", fmt.Errorf("unknown thread")
	}
	ns := sortedTurnNsLocked(dir)
	if len(ns) == 0 {
		return 0, "", fmt.Errorf("unknown thread")
	}
	n := ns[len(ns)-1] + 1
	if err := writeJSONFile(filepath.Join(dir, strconv.Itoa(n)+".json"),
		Turn{TurnID: turnID, Prompt: prompt, ProjectHint: projectHint, Time: time.Now().UTC()}); err != nil {
		return 0, "", err
	}
	return n, turnID, nil
}

// CompleteTurn overwrites N.json with the finished turn.
func (s *Store) CompleteTurn(threadID string, n int, turn Turn) error {
	if !validID(threadID) || n < 1 || turn.TurnID == "" || strings.TrimSpace(turn.Prompt) == "" {
		return fmt.Errorf("bad turn")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(threadID)
	if _, ok := readManifestLocked(dir); !ok {
		return fmt.Errorf("unknown thread")
	}
	found := false
	for _, x := range sortedTurnNsLocked(dir) {
		if x == n {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("unknown turn")
	}
	if turn.Time.IsZero() {
		turn.Time = time.Now().UTC()
	}
	if turn.Steps == nil {
		turn.Steps = []Step{}
	}
	return writeJSONFile(filepath.Join(dir, strconv.Itoa(n)+".json"), turn)
}

// Get returns one thread with all its turns.
func (s *Store) Get(id string) (Thread, error) {
	if !validID(id) {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dir := s.threadDir(id)
	man, ok := readManifestLocked(dir)
	if !ok || man.ID != id {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	ns := sortedTurnNsLocked(dir)
	th := Thread{ID: man.ID, Title: man.Title, CreatedAt: man.CreatedAt, UpdatedAt: man.CreatedAt}
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

// List returns summaries sorted by createdAt, newest first.
func (s *Store) List() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.dir)
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
		dir := filepath.Join(s.dir, e.Name())
		man, ok := readManifestLocked(dir)
		if !ok || man.ID != e.Name() {
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

// Delete removes one thread folder. Idempotent.
func (s *Store) Delete(id string) error {
	if !validID(id) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.threadDir(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
