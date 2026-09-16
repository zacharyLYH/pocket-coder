// Package codemapthreads persists codemap chats as one thread per file.
//
// A thread is one chat: an id, a title, and an ordered turn list. Each
// thread lives in its own JSON file:
//
//	$DATA_DIR/codemaps/<escaped-project>/<title>.json
//
// The thread ID remains the stable API identity; the filename is cosmetic
// and follows the title, with " (1)" etc. for duplicate titles.
//
// The debug-only lineage graph sits beside its thread under the same
// name with a .lineage.json suffix:
//
//	$DATA_DIR/codemaps/<escaped-project>/<title>.lineage.json
//
// Lineage lookup is by threadId inside the JSON (titles can be renamed),
// and the file tracks the thread's current title across renames. Each
// save overwrites the previous lineage for that thread — it is a debug
// artifact for the latest turn, while the thread file keeps every turn.
//
// That is the whole abstraction: users have many chats per project, each
// independently loadable and deletable. The global events.log carries only
// a lightweight audit line per turn (no sections), never the chat itself.
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
	"strings"
	"sync"
	"time"
)

// Turn is one prompt/answer pair inside a thread. Sections and Tools are
// stored as raw JSON so the store never needs to import the codemap
// package (no import cycle with httpapi helpers).
type Turn struct {
	TurnID          string          `json:"turnId"`
	SHA             string          `json:"sha,omitempty"`
	Prompt          string          `json:"prompt"`
	Sections        json.RawMessage `json:"sections,omitempty"`
	Tools           json.RawMessage `json:"tools,omitempty"`
	ExtractorOutput json.RawMessage `json:"extractorOutput,omitempty"`
	Time            time.Time       `json:"time"`
}

// Thread is one chat.
type Thread struct {
	ID        string    `json:"id"`
	Project   string    `json:"project"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Turns     []Turn    `json:"turns"`
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

// Store owns the thread files. Safe for concurrent use.
type Store struct {
	mu  sync.Mutex
	dir string
}

// New returns a store rooted at dir (e.g. $DATA_DIR/codemaps).
// A nil store is usable: all methods no-op with an error, so handlers can
// keep a nil guard in one place.
func New(dir string) *Store {
	return &Store{dir: dir}
}

func MintID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func (s *Store) projectDir(project string) string {
	return filepath.Join(s.dir, url.PathEscape(project))
}

func (s *Store) findThreadPath(project, id string) string {
	entries, err := os.ReadDir(s.projectDir(project))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".lineage.json") {
			continue
		}
		candidate := filepath.Join(s.projectDir(project), e.Name())
		raw, rerr := os.ReadFile(candidate)
		if rerr != nil {
			continue
		}
		var th Thread
		if json.Unmarshal(raw, &th) == nil && th.ID == id && th.Project == project {
			return candidate
		}
	}
	return ""
}

// titleFilename turns the user-visible title into the on-disk thread name.
// Keep the title readable, while preventing it from escaping the project
// directory or becoming a special path. Duplicate titles are disambiguated
// by save with the same " (1)" convention used by desktop operating
// systems.
func titleFilename(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New chat"
	}
	var b strings.Builder
	for _, r := range title {
		switch {
		case r == '/' || r == '\\':
			b.WriteRune('-')
		case r < 0x20 || r == 0x7f:
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.TrimSpace(strings.Trim(b.String(), "."))
	if name == "" {
		name = "New chat"
	}
	return truncate(name, 120)
}

func validID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// truncate caps s at max bytes, marking the cut with an ellipsis.
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
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

// Create starts an empty thread. Title defaults to "New chat" and is
// replaced by the first prompt on the first appended turn.
func (s *Store) Create(project, title string) (Thread, error) {
	if s == nil || s.dir == "" {
		return Thread{}, fmt.Errorf("codemap store not configured")
	}
	if strings.TrimSpace(project) == "" {
		return Thread{}, fmt.Errorf("project is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New chat"
	}
	title = truncate(title, 120)
	now := time.Now().UTC()
	th := Thread{ID: MintID(), Project: project, Title: title, CreatedAt: now, UpdatedAt: now}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.save(th); err != nil {
		return Thread{}, err
	}
	return th, nil
}

// Get loads one thread.
func (s *Store) Get(project, id string) (Thread, error) {
	if s == nil || s.dir == "" {
		return Thread{}, fmt.Errorf("codemap store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(project, id)
}

// List returns summaries for a project, newest first.
func (s *Store) List(project string) ([]Summary, error) {
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("codemap store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.projectDir(project))
	if os.IsNotExist(err) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.projectDir(project), e.Name()))
		if err != nil {
			continue
		}
		var th Thread
		if err := json.Unmarshal(raw, &th); err != nil {
			continue
		}
		if th.Project != project {
			continue
		}
		preview := ""
		if n := len(th.Turns); n > 0 {
			preview = truncate(th.Turns[n-1].Prompt, 120)
		}
		out = append(out, Summary{
			ID: th.ID, Title: th.Title, CreatedAt: th.CreatedAt,
			UpdatedAt: th.UpdatedAt, TurnCount: len(th.Turns), Preview: preview,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// DeleteProjectDir removes every thread and lineage file of one project.
// Project deletion calls it: chats are project-scoped artifacts and must
// not outlive the project. A missing directory is success.
func (s *Store) DeleteProjectDir(project string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("codemap store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.projectDir(project)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// AppendTurn adds one turn to a thread, stamping time and refreshing the
// title when the thread still has the default. Caps history at 200 turns
// (oldest dropped) so files stay small.
func (s *Store) AppendTurn(project, id string, turn Turn) (Thread, error) {
	if s == nil || s.dir == "" {
		return Thread{}, fmt.Errorf("codemap store not configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	th, err := s.read(project, id)
	if err != nil {
		return Thread{}, err
	}
	if strings.TrimSpace(turn.Prompt) == "" {
		return Thread{}, fmt.Errorf("prompt is required")
	}
	if turn.Time.IsZero() {
		turn.Time = time.Now().UTC()
	}
	th.Turns = append(th.Turns, turn)
	if len(th.Turns) > 200 {
		th.Turns = th.Turns[len(th.Turns)-200:]
	}
	oldTitle := th.Title
	if th.Title == "" || th.Title == "New chat" {
		th.Title = TitleFromPrompt(turn.Prompt)
	}
	th.UpdatedAt = time.Now().UTC()
	if err := s.save(th); err != nil {
		return Thread{}, err
	}
	// The lineage file follows the title: the first prompt renames both.
	if th.Title != oldTitle {
		s.syncLineageNamesLocked(project, id, th.Title)
	}
	return th, nil
}

// Rename sets a thread's title.
func (s *Store) Rename(project, id, title string) (Thread, error) {
	if s == nil || s.dir == "" {
		return Thread{}, fmt.Errorf("codemap store not configured")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		return Thread{}, fmt.Errorf("title is required")
	}
	title = truncate(title, 120)
	s.mu.Lock()
	defer s.mu.Unlock()
	th, err := s.read(project, id)
	if err != nil {
		return Thread{}, err
	}
	oldTitle := th.Title
	th.Title = title
	th.UpdatedAt = time.Now().UTC()
	if err := s.save(th); err != nil {
		return Thread{}, err
	}
	// Keep the debug artifact beside the thread it belongs to.
	if title != oldTitle {
		s.syncLineageNamesLocked(project, id, title)
	}
	return th, nil
}

// Delete removes one thread. Missing threads are a success (idempotent).
func (s *Store) Delete(project, id string) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("codemap store not configured")
	}
	if !validID(id) {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if threadPath := s.findThreadPath(project, id); threadPath != "" {
		if err := os.Remove(threadPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	// Lineage files are keyed by thread ID. Remove every artifact belonging to
	// this thread as well; old files without metadata are left untouched.
	entries, readErr := os.ReadDir(s.projectDir(project))
	if readErr == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".lineage.json") {
				continue
			}
			raw, rerr := os.ReadFile(filepath.Join(s.projectDir(project), e.Name()))
			if rerr != nil {
				continue
			}
			var meta struct {
				ThreadID string `json:"threadId"`
			}
			if json.Unmarshal(raw, &meta) == nil && meta.ThreadID == id {
				if err := os.Remove(filepath.Join(s.projectDir(project), e.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	} else if !os.IsNotExist(readErr) {
		return readErr
	}
	return nil
}

// lineagePath is the on-disk name for a thread's lineage file: the
// thread title plus a .lineage.json suffix, mirroring the thread file.
func (s *Store) lineagePath(project, title string) string {
	return filepath.Join(s.projectDir(project), titleFilename(title)+".lineage.json")
}

// findLineagePaths returns every lineage file in the project dir whose
// threadId matches. Titles can be renamed, so lookup is a scan of the
// JSON, not a name computation.
func (s *Store) findLineagePaths(project, threadID string) []string {
	dir := s.projectDir(project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lineage.json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		var meta struct {
			ThreadID string `json:"threadId"`
		}
		if json.Unmarshal(raw, &meta) == nil && meta.ThreadID == threadID {
			out = append(out, path)
		}
	}
	return out
}

// syncLineageNamesLocked points every lineage file of threadID at the
// thread's current title, so renames move the debug artifact together
// with the thread file. Stale duplicates are removed. Callers hold s.mu.
func (s *Store) syncLineageNamesLocked(project, threadID, title string) {
	target := s.lineagePath(project, title)
	for _, path := range s.findLineagePaths(project, threadID) {
		if path == target {
			continue
		}
		if _, statErr := os.Stat(target); statErr == nil {
			_ = os.Remove(path)
			continue
		}
		_ = os.Rename(path, target)
	}
}

// SaveLineage writes the debug-only lineage beside its FE thread file,
// under the same title-derived name with a .lineage.json suffix, and
// prunes old lineage artifacts without touching the conversation record.
func (s *Store) SaveLineage(project, threadID, title string, raw []byte) error {
	if s == nil || s.dir == "" {
		return fmt.Errorf("codemap store not configured")
	}
	if !validID(threadID) {
		return fmt.Errorf("invalid thread id")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.projectDir(project), 0o700); err != nil {
		return err
	}
	path := s.lineagePath(project, title)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// Stale lineage under an old title (rename between turns) must not
	// linger beside the fresh file.
	s.syncLineageNamesLocked(project, threadID, title)
	return s.pruneLineageLocked(project, 200)
}

// ReadLineage loads one debug artifact. It is intentionally not used by the
// frontend thread endpoints, but is useful to diagnostics and tests.
func (s *Store) ReadLineage(project, threadID string) ([]byte, error) {
	if s == nil || s.dir == "" {
		return nil, fmt.Errorf("codemap store not configured")
	}
	if !validID(threadID) {
		return nil, fmt.Errorf("unknown lineage")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, path := range s.findLineagePaths(project, threadID) {
		raw, err := os.ReadFile(path)
		if err == nil {
			return raw, nil
		}
	}
	return nil, fmt.Errorf("unknown lineage")
}

func (s *Store) pruneLineageLocked(project string, keep int) error {
	entries, err := os.ReadDir(s.projectDir(project))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type item struct {
		name string
		mod  time.Time
	}
	var files []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".lineage.json") {
			continue
		}
		info, ierr := e.Info()
		if ierr == nil {
			files = append(files, item{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	if len(files) <= keep {
		return nil
	}
	for _, f := range files[keep:] {
		if err := os.Remove(filepath.Join(s.projectDir(project), f.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// read loads one thread file. Callers hold s.mu.
func (s *Store) read(project, id string) (Thread, error) {
	if !validID(id) {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	dir := s.projectDir(project)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Thread{}, fmt.Errorf("unknown thread")
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".lineage.json") {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		var th Thread
		if json.Unmarshal(raw, &th) == nil && th.ID == id && th.Project == project {
			return th, nil
		}
	}
	return Thread{}, fmt.Errorf("unknown thread")
}

func (s *Store) save(th Thread) error {
	if err := os.MkdirAll(s.projectDir(th.Project), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(th, "", "  ")
	if err != nil {
		return err
	}
	dir := s.projectDir(th.Project)
	oldPath := s.findThreadPath(th.Project, th.ID)
	base := titleFilename(th.Title)
	filename := base + ".json"
	for n := 1; ; n++ {
		candidate := filepath.Join(dir, filename)
		if candidate == oldPath {
			break
		}
		if _, statErr := os.Stat(candidate); os.IsNotExist(statErr) {
			break
		}
		filename = fmt.Sprintf("%s (%d).json", base, n)
	}
	path := filepath.Join(dir, filename)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if oldPath != "" && oldPath != path {
		if err := os.Remove(oldPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
