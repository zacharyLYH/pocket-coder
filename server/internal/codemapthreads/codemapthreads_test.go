package codemapthreads

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	th, err := s.Create("owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if th.Title != "New chat" {
		t.Fatalf("default title = %q", th.Title)
	}
	th, err = s.AppendTurn("owner/repo", th.ID, Turn{Prompt: "where is main?"})
	if err != nil {
		t.Fatal(err)
	}
	if th.Title != "where is main?" {
		t.Fatalf("title should derive from first prompt, got %q", th.Title)
	}
	got, err := s.Get("owner/repo", th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Turns) != 1 || got.Turns[0].Prompt != "where is main?" {
		t.Fatalf("turns = %+v", got.Turns)
	}
	list, err := s.List("owner/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].TurnCount != 1 || list[0].Title != "where is main?" {
		t.Fatalf("list = %+v", list)
	}
	// Project isolation: slashed ids escape to one dir, never leak.
	if other, err := s.List("owner/other"); err != nil || len(other) != 0 {
		t.Fatalf("cross-project leak: %v %v", other, err)
	}
	if _, err := s.Get("owner/other", th.ID); err == nil {
		t.Fatal("thread readable under wrong project")
	}
	if err := s.Delete("owner/repo", th.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List("owner/repo"); len(list) != 0 {
		t.Fatalf("after delete list = %+v", list)
	}
	if err := s.Delete("owner/repo", th.ID); err != nil {
		t.Fatalf("second delete should succeed: %v", err)
	}
}

func TestThreadFilesUseTitlesAndDisambiguateDuplicates(t *testing.T) {
	s := New(t.TempDir())
	a, err := s.Create("owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.AppendTurn("owner/repo", a.ID, Turn{Prompt: "same title"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create("owner/repo", "same title")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(s.projectDir("owner/repo"))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && !strings.HasSuffix(entry.Name(), ".lineage.json") {
			names[entry.Name()] = true
		}
	}
	if !names["same title.json"] || !names["same title (1).json"] {
		t.Fatalf("thread filenames = %v, want title and duplicate suffix", names)
	}
	if _, err := s.Get("owner/repo", a.ID); err != nil {
		t.Fatalf("title-renamed thread unreadable: %v", err)
	}
	if _, err := s.Get("owner/repo", b.ID); err != nil {
		t.Fatalf("duplicate thread unreadable: %v", err)
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

// Tool steps with outputs round-trip verbatim: the thread file is what
// follow-ups replay, so outputs/errors must survive the store.
func TestStoreToolStepsRoundTrip(t *testing.T) {
	s := New(t.TempDir())
	th, err := s.Create("owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	tools, _ := json.Marshal([]map[string]any{
		{"tool": "search_code", "args": `{"pattern":"main"}`, "output": "main.go:10:func main() {"},
		{"tool": "read_file", "args": `{"path":"main.go"}`, "error": "boom"},
	})
	sections, _ := json.Marshal([]map[string]any{{"title": "Auth"}})
	th, err = s.AppendTurn("owner/repo", th.ID, Turn{
		Prompt: "where is main?", Sections: sections, Tools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("owner/repo", th.ID)
	if err != nil {
		t.Fatal(err)
	}
	var steps []map[string]any
	if err := json.Unmarshal(got.Turns[0].Tools, &steps); err != nil {
		t.Fatal(err)
	}
	if len(steps) != 2 {
		t.Fatalf("steps = %v, want 2", steps)
	}
	if steps[0]["output"] != "main.go:10:func main() {" {
		t.Fatalf("output lost: %v", steps[0])
	}
	if steps[1]["error"] != "boom" {
		t.Fatalf("error lost: %v", steps[1])
	}
	// Old shape ({tool,args} without output) still parses.
	old, _ := json.Marshal([]map[string]any{{"tool": "search_code", "args": `{"pattern":"x"}`}})
	if _, err := s.AppendTurn("owner/repo", th.ID, Turn{Prompt: "again?", Tools: old}); err != nil {
		t.Fatal(err)
	}
}

func TestLineageRoundTripPruneAndDelete(t *testing.T) {
	s := New(t.TempDir())
	th, err := s.Create("owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveLineage("owner/repo", th.ID, th.Title, []byte(`{"threadId":"`+th.ID+`","initialRequest":{"tools":["search_code"]},"events":[]}`)); err != nil {
		t.Fatal(err)
	}
	path := s.lineagePath("owner/repo", th.Title)
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), "initialRequest") {
		t.Fatalf("lineage read = %q, %v", raw, err)
	}
	if err := s.Delete("owner/repo", th.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("lineage after delete: %v", err)
	}
}
