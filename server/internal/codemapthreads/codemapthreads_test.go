package codemapthreads

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReserveNewThreadLayout(t *testing.T) {
	s := New(t.TempDir())
	tid, turnID, err := s.ReserveNewThread("owner/repo", "where is main?", "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if tid == "" || turnID == "" {
		t.Fatalf("ids empty: %q %q", tid, turnID)
	}
	dir := filepath.Join(s.projectDir("owner/repo"), tid)
	// Manifest is exactly {id, title, createdAt}.
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man map[string]any
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if len(man) != 3 || man["id"] != tid {
		t.Fatalf("manifest keys = %v, want exactly id/title/createdAt", man)
	}
	if _, ok := man["project"]; ok {
		t.Fatalf("manifest must not carry project: %v", man)
	}
	for _, bad := range []string{"updatedAt", "nextTurnIndex", "turnCount", "preview"} {
		if _, ok := man[bad]; ok {
			t.Fatalf("manifest must not carry %q: %v", bad, man)
		}
	}
	if man["title"] != "where is main?" {
		t.Fatalf("title = %v", man["title"])
	}
	// 1.json placeholder: bare fields, sections/tools/error null.
	praw, err := os.ReadFile(filepath.Join(dir, "1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ph map[string]any
	if err := json.Unmarshal(praw, &ph); err != nil {
		t.Fatal(err)
	}
	if ph["turnId"] != turnID || ph["prompt"] != "where is main?" || ph["sha"] != "abc123" {
		t.Fatalf("placeholder = %v", ph)
	}
	for _, k := range []string{"sections", "tools", "error"} {
		v, ok := ph[k]
		if !ok || v != nil {
			t.Fatalf("placeholder %q = %v (present=%v), want explicit null", k, v, ok)
		}
	}
	// No lineage placeholder.
	if _, err := os.Stat(filepath.Join(dir, "1.lineage.json")); !os.IsNotExist(err) {
		t.Fatalf("lineage placeholder must not exist: %v", err)
	}
	// No ExtractorOutput anywhere in the turn.
	if strings.Contains(string(praw), "extractorOutput") || strings.Contains(string(praw), "extractorInput") {
		t.Fatalf("turn must not carry extractor keys: %s", praw)
	}
}

func TestFollowupMaxPlusOneAndNumericSort(t *testing.T) {
	s := New(t.TempDir())
	tid, _, err := s.ReserveNewThread("p", "first?", "s")
	if err != nil {
		t.Fatal(err)
	}
	// Complete turn 1 so the thread has sections.
	sec, _ := json.Marshal([]map[string]any{{"title": "A"}})
	tools, _ := json.Marshal([]map[string]any{})
	if err := s.CompleteTurn("p", tid, 1, Turn{TurnID: "t1", SHA: "s", Prompt: "first?", Sections: sec, Tools: tools, Time: time.Now().UTC()}, []byte(`{"threadId":"x"}`)); err != nil {
		t.Fatal(err)
	}
	// Reserve follow-ups up to 11 to pin numeric (not lexical) sort past 9.
	for i := 2; i <= 11; i++ {
		n, _, err := s.ReserveFollowup("p", tid, "q"+strconv.Itoa(i), "s")
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Fatalf("reserve %d got N=%d", i, n)
		}
		if err := s.CompleteTurn("p", tid, n, Turn{TurnID: "t" + strconv.Itoa(i), Prompt: "q" + strconv.Itoa(i), Sections: sec, Tools: tools, Time: time.Now().UTC()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	th, err := s.Get("p", tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 11 {
		t.Fatalf("turns = %d, want 11", len(th.Turns))
	}
	for i, turn := range th.Turns {
		if turn.Prompt != "q"+strconv.Itoa(i+1) && !(i == 0 && turn.Prompt == "first?") {
			t.Fatalf("turn %d out of order: %q", i, turn.Prompt)
		}
	}
	list, err := s.List("p")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TurnCount != 11 {
		t.Fatalf("list = %+v", list)
	}
}

func TestPlaceholderOverwriteAndErrorFill(t *testing.T) {
	s := New(t.TempDir())
	tid, turnID, err := s.ReserveNewThread("p", "map this repo", "s")
	if err != nil {
		t.Fatal(err)
	}
	// Failure fills error in the same file; placeholder stays visible.
	if err := s.FailTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s", Prompt: "map this repo", Time: time.Now().UTC()}, "boom", []byte(`{"prunedTier1Data":{"answer":"x"},"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	th, err := s.Get("p", tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 || th.Turns[0].TurnID != turnID {
		t.Fatalf("turnId changed on fail: %+v", th.Turns)
	}
	if th.Turns[0].Error == nil || *th.Turns[0].Error != "boom" {
		t.Fatalf("error not filled: %+v", th.Turns[0])
	}
	lin, err := s.ReadTurnLineage("p", tid, 1)
	if err != nil || !strings.Contains(string(lin), "prunedTier1Data") {
		t.Fatalf("lineage = %q, %v", lin, err)
	}
	if strings.Contains(string(lin), "extractorInput") || strings.Contains(string(lin), "extractorOutput") {
		t.Fatalf("lineage must use prunedTier1Data, never extractor keys: %s", lin)
	}
}

func TestBeginRetryKeepsTurnIDAndPath(t *testing.T) {
	s := New(t.TempDir())
	tid, turnID, err := s.ReserveNewThread("p", "first?", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FailTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s1", Prompt: "first?", Time: time.Now().UTC()}, "boom", []byte(`{"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	n, rid, prompt, err := s.BeginRetry("p", tid, "s2")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || rid != turnID || prompt != "first?" {
		t.Fatalf("retry = %d %q %q, want 1 %q first?", n, rid, prompt, turnID)
	}
	th, err := s.Get("p", tid)
	if err != nil {
		t.Fatal(err)
	}
	got := th.Turns[0]
	if got.TurnID != turnID || got.Prompt != "first?" || got.SHA != "s2" {
		t.Fatalf("retry placeholder = %+v", got)
	}
	if got.Error != nil {
		t.Fatalf("retry must clear error: %+v", got)
	}
	if _, err := s.ReadTurnLineage("p", tid, 1); err == nil {
		t.Fatalf("retry must drop the old lineage")
	}
	// Success path rewrites the same path.
	sec, _ := json.Marshal([]map[string]any{{"title": "A"}})
	if err := s.CompleteTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s2", Prompt: "first?", Sections: sec, Time: time.Now().UTC()}, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	th, _ = s.Get("p", tid)
	if len(th.Turns) != 1 || th.Turns[0].TurnID != turnID {
		t.Fatalf("retry complete = %+v", th.Turns)
	}
}

func TestManifestNeverBumpsAndListSortsByCreatedAt(t *testing.T) {
	s := New(t.TempDir())
	a, _, _ := s.ReserveNewThread("p", "aaa?", "s")
	manA, _ := os.ReadFile(filepath.Join(s.projectDir("p"), a, "manifest.json"))
	time.Sleep(10 * time.Millisecond)
	b, _, _ := s.ReserveNewThread("p", "bbb?", "s")
	// Follow-up on the older thread must not reorder the list.
	n, _, _ := s.ReserveFollowup("p", a, "aaa again?", "s")
	sec, _ := json.Marshal([]map[string]any{{"title": "A"}})
	_ = s.CompleteTurn("p", a, n, Turn{TurnID: "x", Prompt: "aaa again?", Sections: sec, Time: time.Now().UTC()}, nil)
	manA2, _ := os.ReadFile(filepath.Join(s.projectDir("p"), a, "manifest.json"))
	if string(manA) != string(manA2) {
		t.Fatalf("manifest bumped on turn:\n%s\n%s", manA, manA2)
	}
	list, _ := s.List("p")
	if len(list) != 2 || list[0].ID != b || list[1].ID != a {
		t.Fatalf("list order = %+v, want newest-created first [%s %s]", list, b, a)
	}
	// Preview comes from 1.json first prompt (truncate 120); updatedAt
	// derives from the last turn time.
	if list[1].Preview != "aaa?" {
		t.Fatalf("preview = %q", list[1].Preview)
	}
	if !list[1].UpdatedAt.After(list[1].CreatedAt) {
		t.Fatalf("updatedAt not derived: %+v", list[1])
	}
}

func TestLegacyFlatFilesSwept(t *testing.T) {
	s := New(t.TempDir())
	dir := s.projectDir("p")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Old Title.json", "Old Title.lineage.json", "f86d778b3a9860570d0972ef.lineage.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tid, _, err := s.ReserveNewThread("p", "hi?", "s")
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("legacy flat file survived: %s (entries=%v, tid=%s)", e.Name(), entries, tid)
		}
	}
	// A thread folder must still exist.
	if _, err := os.Stat(filepath.Join(dir, tid, "manifest.json")); err != nil {
		t.Fatal(err)
	}
}

func TestGetSkipsManifestOnlyAndBadIDs(t *testing.T) {
	s := New(t.TempDir())
	// Manifest-only folder (reserve crash window): not visible.
	id := MintID()
	dir := filepath.Join(s.projectDir("p"), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	man, _ := json.Marshal(Manifest{ID: id, Title: "x", CreatedAt: time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), append(man, '\n'), 0o600)
	if _, err := s.Get("p", id); err == nil {
		t.Fatalf("manifest-only folder must 404")
	}
	if list, _ := s.List("p"); len(list) != 0 {
		t.Fatalf("manifest-only folder listed: %+v", list)
	}
	// Non-hex ids (traversal, legacy titles) never resolve.
	for _, bad := range []string{"nope", "../x", "abc", "New chat", ""} {
		if _, err := s.Get("p", bad); err == nil {
			t.Fatalf("Get(%q) must fail", bad)
		}
	}
	// Delete is idempotent, even for bad ids.
	if err := s.Delete("p", "nope"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreTitle(t *testing.T) {
	for in, want := range map[string]string{
		"":                        "New chat",
		"  spaced   out  ":        "spaced out",
		"first line\nsecond line": "first line",
	} {
		if got := TitleFromPrompt(in); got != want {
			t.Errorf("TitleFromPrompt(%q) = %q, want %q", in, got, want)
		}
	}
	if got := TitleFromPrompt(strings.Repeat("x", 200)); got != strings.Repeat("x", 60)+"…" {
		t.Errorf("long title = %q, want 60 x's plus ellipsis", got)
	}
}

// Tool steps with outputs round-trip verbatim: the turn file is what
// follow-ups replay, so outputs/errors must survive the store.
func TestStoreToolStepsRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	tid, turnID, err := s.ReserveNewThread("owner/repo", "where is main?", "s")
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := json.Marshal([]map[string]any{
		{"thought": "", "steps": []map[string]any{
			{"tool": "search_code", "args": `{"pattern":"main"}`, "output": "main.go:10:func main() {"},
			{"tool": "read_file", "args": `{"path":"main.go"}`, "error": "boom"},
		}},
	})
	sections, _ := json.Marshal([]map[string]any{{"title": "Auth"}})
	if err := s.CompleteTurn("owner/repo", tid, 1, Turn{
		TurnID: turnID, Prompt: "where is main?", Sections: sections, Tools: tools, Time: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("owner/repo", tid)
	if err != nil {
		t.Fatal(err)
	}
	var rounds []map[string]any
	if err := json.Unmarshal(got.Turns[0].Tools, &rounds); err != nil {
		t.Fatal(err)
	}
	steps, _ := rounds[0]["steps"].([]any)
	if len(steps) != 2 {
		t.Fatalf("steps = %v, want 2", steps)
	}
	if steps[0].(map[string]any)["output"] != "main.go:10:func main() {" {
		t.Fatalf("output lost: %v", steps[0])
	}
	if steps[1].(map[string]any)["error"] != "boom" {
		t.Fatalf("error lost: %v", steps[1])
	}
}

func TestDeleteRemovesFolder(t *testing.T) {
	s := New(t.TempDir())
	tid, _, _ := s.ReserveNewThread("p", "hi?", "s")
	if err := s.Delete("p", tid); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.projectDir("p"), tid)); !os.IsNotExist(err) {
		t.Fatalf("folder survived: %v", err)
	}
	if err := s.Delete("p", tid); err != nil {
		t.Fatalf("second delete should succeed: %v", err)
	}
	if list, _ := s.List("p"); len(list) != 0 {
		t.Fatalf("after delete list = %+v", list)
	}
}
