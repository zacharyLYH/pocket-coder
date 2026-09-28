package threads

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReserveGetListDeleteNoScope(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, _, err := s.ReserveNewThread("", "brief me", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.Get("", tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 || th.Turns[0].Prompt != "brief me" || th.Turns[0].ProjectHint != "" {
		t.Fatalf("turns = %+v", th.Turns)
	}
	if th.Title != "brief me" {
		t.Fatalf("title = %q", th.Title)
	}
	n, _, err := s.ReserveFollowup("", tid, "and now?", "", nil)
	if err != nil || n != 2 {
		t.Fatalf("followup n=%d err=%v", n, err)
	}
	if err := s.CompleteTurn("", tid, 2, Turn{TurnID: "t2", Prompt: "and now?", Time: th.CreatedAt}, nil); err != nil {
		t.Fatal(err)
	}
	list, err := s.List("")
	if err != nil || len(list) != 1 || list[0].TurnCount != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := s.Delete("", tid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("", tid); err == nil {
		t.Fatal("get after delete must fail")
	}
	if list, _ := s.List(""); len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestReserveGetListDeleteWithScope(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, _, err := s.ReserveNewThread("owner/repo", "where is main?", "abc123", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Scoped folder layout: <dir>/<escaped-scope>/<threadID>.
	if _, err := os.Stat(filepath.Join(s.dir, urlPathEscape("owner/repo"), tid, "manifest.json")); err != nil {
		t.Fatalf("scoped layout: %v", err)
	}
	if _, err := s.Get("other", tid); err == nil {
		t.Fatal("cross-scope get must fail")
	}
	th, err := s.Get("owner/repo", tid)
	if err != nil || len(th.Turns) != 1 || th.Turns[0].SHA != "abc123" {
		t.Fatalf("th = %+v err=%v", th, err)
	}
	// DeleteScope wipes the whole scope.
	if err := s.DeleteScope("owner/repo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("owner/repo", tid); err == nil {
		t.Fatal("get after DeleteScope must fail")
	}
	// DeleteScope refuses the empty scope (never wipe the store root).
	if err := s.DeleteScope(""); err == nil {
		t.Fatal("DeleteScope(\"\") must be refused")
	}
}

func TestCompleteTurnWritesSeparateLineage(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, _, err := s.ReserveNewThread("", "brief me", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTurn("", tid, 1, Turn{TurnID: "t1", Prompt: "brief me"}, []byte(`{"events":[{"kind":"tool_start"}]}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadTurnLineage("", tid, 1); err != nil || !strings.Contains(string(got), "tool_start") {
		t.Fatalf("lineage = %s, err=%v", got, err)
	}
}

func TestFailedTurnPersistsError(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, turnID, err := s.ReserveNewThread("p", "map this repo", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Failure = CompleteTurn with Error filled; placeholder stays visible.
	msg := "boom"
	if err := s.CompleteTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s", Prompt: "map this repo", Time: time.Now().UTC(), Error: &msg}, []byte(`{"events":[]}`)); err != nil {
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
	if err != nil || !strings.Contains(string(lin), "events") {
		t.Fatalf("lineage = %q, %v", lin, err)
	}
}

func TestBeginRetryKeepsTurnIDAndPath(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, turnID, err := s.ReserveNewThread("p", "first?", "s1", nil)
	if err != nil {
		t.Fatal(err)
	}
	msg := "boom"
	if err := s.CompleteTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s1", Prompt: "first?", Time: time.Now().UTC(), Error: &msg}, []byte(`{"events":[]}`)); err != nil {
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
		t.Fatal("retry must drop the old lineage")
	}
	// Success path rewrites the same path.
	payload, _ := json.Marshal(map[string]any{"sections": []map[string]any{{"title": "A"}}})
	if err := s.CompleteTurn("p", tid, 1, Turn{TurnID: turnID, SHA: "s2", Prompt: "first?", Payload: payload, Time: time.Now().UTC()}, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	th, _ = s.Get("p", tid)
	if len(th.Turns) != 1 || th.Turns[0].TurnID != turnID {
		t.Fatalf("retry complete = %+v", th.Turns)
	}
}

func TestApprovalsSurviveReloadAndCanBeRemoved(t *testing.T) {
	dir := t.TempDir()
	s := New(dir, true)
	tid, _, err := s.ReserveNewThread("", "setup", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	a := Approval{ID: MintID(), TurnID: "turn-1", Tool: "stop", Args: json.RawMessage(`{"project":"a/b"}`), Summary: "Stop a/b?", BlastRadius: "Container stops."}
	if err := s.AddApproval(tid, a); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(dir, true).Approvals(tid)
	if err != nil || len(loaded) != 1 || loaded[0].ID != a.ID {
		t.Fatalf("reloaded approvals = %+v, err=%v", loaded, err)
	}
	thread, err := New(dir, true).Get("", tid)
	if err != nil || len(thread.Approvals) != 1 {
		t.Fatalf("thread approvals = %+v, err=%v", thread.Approvals, err)
	}
	if err := New(dir, true).ResolveApproval(tid, a.ID, ApprovalDiscarded); err != nil {
		t.Fatal(err)
	}
	if got, _ := New(dir, true).Approvals(tid); len(got) != 1 || got[0].Status != ApprovalDiscarded || got[0].ResolvedAt.IsZero() {
		t.Fatalf("approvals after resolve = %+v", got)
	}
	if n := New(dir, true).ApprovalCount(); n != 0 {
		t.Fatalf("ApprovalCount after resolve = %d, want 0", n)
	}
}

func TestPendingApprovalBlocksFollowup(t *testing.T) {
	s := New(t.TempDir(), true)
	tid, turnID, err := s.ReserveNewThread("", "stop it", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddApproval(tid, Approval{ID: MintID(), TurnID: turnID, Tool: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveFollowup("", tid, "what now?", "", nil); !errors.Is(err, ErrPendingApproval) {
		t.Fatalf("followup error = %v, want pending approval", err)
	}
	approvals, _ := s.Approvals(tid)
	if len(approvals) != 1 || approvals[0].TurnID != turnID {
		t.Fatalf("approvals = %+v, want turn id %q", approvals, turnID)
	}
}

func TestNoApprovalsWhenDisabled(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, _, err := s.ReserveNewThread("", "hi", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddApproval(tid, Approval{ID: "x", Tool: "y"}); err == nil {
		t.Fatal("AddApproval must fail when approvals disabled")
	}
	if _, err := s.Approvals(tid); err == nil {
		t.Fatal("Approvals must fail when approvals disabled")
	}
	// Reserve is not blocked.
	if _, _, err := s.ReserveFollowup("", tid, "next?", "", nil); err != nil {
		t.Fatalf("reserve blocked without approvals: %v", err)
	}
}

func TestBadIDsAndTitles(t *testing.T) {
	s := New(t.TempDir(), false)
	for _, bad := range []string{"nope", "../x", "", "New chat"} {
		if _, err := s.Get("", bad); err == nil {
			t.Fatalf("Get(%q) must fail", bad)
		}
	}
	if err := s.Delete("", "nope"); err != nil {
		t.Fatal(err)
	}
	if TitleFromPrompt("") != "New chat" {
		t.Fatal("empty title")
	}
	if got := TitleFromPrompt(strings.Repeat("x", 200)); got != strings.Repeat("x", 60)+"\u2026" {
		t.Fatalf("long title = %q", got)
	}
	if got := TitleFromPrompt("first line\nsecond line"); got != "first line" {
		t.Fatalf("multiline title = %q", got)
	}
	if _, _, err := s.ReserveNewThread("", "  ", "", nil); err == nil {
		t.Fatal("blank prompt must fail")
	}
}

func TestManifestNeverBumpsAndListSortsByCreatedAt(t *testing.T) {
	s := New(t.TempDir(), false)
	a, _, _ := s.ReserveNewThread("", "aaa?", "", nil)
	manA, _ := os.ReadFile(filepath.Join(s.dir, a, "manifest.json"))
	time.Sleep(10 * time.Millisecond)
	b, _, _ := s.ReserveNewThread("", "bbb?", "", nil)
	// Follow-up on the older thread must not reorder the list.
	n, _, _ := s.ReserveFollowup("", a, "aaa again?", "", nil)
	_ = s.CompleteTurn("", a, n, Turn{TurnID: "x", Prompt: "aaa again?", Time: time.Now().UTC()}, nil)
	manA2, _ := os.ReadFile(filepath.Join(s.dir, a, "manifest.json"))
	if string(manA) != string(manA2) {
		t.Fatalf("manifest bumped on turn:\n%s\n%s", manA, manA2)
	}
	list, _ := s.List("")
	if len(list) != 2 || list[0].ID != b || list[1].ID != a {
		t.Fatalf("list order = %+v, want newest-created first [%s %s]", list, b, a)
	}
	if list[1].Preview != "aaa?" {
		t.Fatalf("preview = %q", list[1].Preview)
	}
	if !list[1].UpdatedAt.After(list[1].CreatedAt) {
		t.Fatalf("updatedAt not derived: %+v", list[1])
	}
}

func TestFollowupMaxPlusOneAndNumericSort(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, _, err := s.ReserveNewThread("", "first?", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Reserve follow-ups up to 11 to pin numeric (not lexical) sort past 9.
	for i := 2; i <= 11; i++ {
		n, _, err := s.ReserveFollowup("", tid, "q"+strconv.Itoa(i), "", nil)
		if err != nil {
			t.Fatal(err)
		}
		if n != i {
			t.Fatalf("reserve %d got N=%d", i, n)
		}
		if err := s.CompleteTurn("", tid, n, Turn{TurnID: "t" + strconv.Itoa(i), Prompt: "q" + strconv.Itoa(i), Time: time.Now().UTC()}, nil); err != nil {
			t.Fatal(err)
		}
	}
	th, err := s.Get("", tid)
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
}

// Payload round-trips verbatim: the turn file is what follow-ups replay,
// so the product's payload must survive the store untouched.
func TestPayloadRoundTrip(t *testing.T) {
	s := New(t.TempDir(), false)
	tid, turnID, err := s.ReserveNewThread("owner/repo", "where is main?", "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"sections": []map[string]any{{"title": "Auth"}},
		"tools": []map[string]any{
			{"thought": "", "steps": []map[string]any{
				{"tool": "search_code", "args": `{"pattern":"main"}`, "output": "main.go:10:func main() {"},
				{"tool": "read_file", "args": `{"path":"main.go"}`, "error": "boom"},
			}},
		},
	})
	if err := s.CompleteTurn("owner/repo", tid, 1, Turn{
		TurnID: turnID, Prompt: "where is main?", Payload: payload, Time: time.Now().UTC(),
	}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("owner/repo", tid)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got.Turns[0].Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	tools, _ := decoded["tools"].([]any)
	round, _ := tools[0].(map[string]any)
	steps, _ := round["steps"].([]any)
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

func TestGetSkipsManifestOnlyAndUnreadableTurns(t *testing.T) {
	s := New(t.TempDir(), false)
	// Manifest-only folder (reserve crash window): not visible.
	id := MintID()
	dir := filepath.Join(s.dir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	man, _ := json.Marshal(Manifest{ID: id, Title: "x", CreatedAt: time.Now().UTC()})
	_ = os.WriteFile(filepath.Join(dir, "manifest.json"), append(man, '\n'), 0o600)
	if _, err := s.Get("", id); err == nil {
		t.Fatal("manifest-only folder must 404")
	}
	if list, _ := s.List(""); len(list) != 0 {
		t.Fatalf("manifest-only folder listed: %+v", list)
	}
	// A thread whose only turn is unreadable (old shape, torn write):
	// Get and List skip it, never crash.
	tid, _, _ := s.ReserveNewThread("", "ok?", "", nil)
	if err := os.WriteFile(filepath.Join(s.dir, tid, "1.json"), []byte(`{"prompt":"legacy no turnId"`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("", tid); err == nil {
		t.Fatal("thread with no readable turns must 404")
	}
	if list, _ := s.List(""); len(list) != 0 {
		t.Fatalf("unreadable thread listed: %+v", list)
	}
	// Non-hex ids (traversal, legacy titles) never resolve.
	for _, bad := range []string{"nope", "../x", "abc", "New chat", ""} {
		if _, err := s.Get("", bad); err == nil {
			t.Fatalf("Get(%q) must fail", bad)
		}
	}
	// Delete is idempotent, even for bad ids.
	if err := s.Delete("", "nope"); err != nil {
		t.Fatal(err)
	}
}
