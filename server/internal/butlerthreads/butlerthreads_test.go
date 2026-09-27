package butlerthreads

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReserveGetListDelete(t *testing.T) {
	s := New(t.TempDir())
	tid, _, err := s.ReserveNewThread("brief me", "a/b")
	if err != nil {
		t.Fatal(err)
	}
	th, err := s.Get(tid)
	if err != nil {
		t.Fatal(err)
	}
	if len(th.Turns) != 1 || th.Turns[0].Prompt != "brief me" || th.Turns[0].ProjectHint != "a/b" {
		t.Fatalf("turns = %+v", th.Turns)
	}
	if th.Title != "brief me" {
		t.Fatalf("title = %q", th.Title)
	}
	n, _, err := s.ReserveFollowup(tid, "and now?", "")
	if err != nil || n != 2 {
		t.Fatalf("followup n=%d err=%v", n, err)
	}
	if err := s.CompleteTurn(tid, 2, Turn{TurnID: "t2", Prompt: "and now?", Answer: "still fine", Time: th.CreatedAt}, nil); err != nil {
		t.Fatal(err)
	}
	list, err := s.List()
	if err != nil || len(list) != 1 || list[0].TurnCount != 2 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if err := s.Delete(tid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(tid); err == nil {
		t.Fatal("get after delete must fail")
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Fatalf("list after delete = %+v", list)
	}
}

func TestCompleteTurnWritesSeparateLineage(t *testing.T) {
	s := New(t.TempDir())
	tid, _, err := s.ReserveNewThread("brief me", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteTurn(tid, 1, Turn{TurnID: "t1", Prompt: "brief me", Answer: "done"}, []byte(`{"events":[{"kind":"tool_start"}]}`)); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ReadTurnLineage(tid, 1); err != nil || !strings.Contains(string(got), "tool_start") {
		t.Fatalf("lineage = %s, err=%v", got, err)
	}
	th, err := s.Get(tid)
	if err != nil || len(th.Turns[0].Steps) != 0 {
		t.Fatalf("turn = %+v, err=%v", th.Turns, err)
	}
}

func TestApprovalsSurviveReloadAndCanBeRemoved(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	tid, _, err := s.ReserveNewThread("setup", "")
	if err != nil {
		t.Fatal(err)
	}
	a := Approval{ID: MintID(), TurnID: "turn-1", Tool: "stop", Args: json.RawMessage(`{"project":"a/b"}`), Summary: "Stop a/b?", BlastRadius: "Container stops."}
	if err := s.AddApproval(tid, a); err != nil {
		t.Fatal(err)
	}
	loaded, err := New(dir).Approvals(tid)
	if err != nil || len(loaded) != 1 || loaded[0].ID != a.ID {
		t.Fatalf("reloaded approvals = %+v, err=%v", loaded, err)
	}
	thread, err := New(dir).Get(tid)
	if err != nil || len(thread.Approvals) != 1 {
		t.Fatalf("thread approvals = %+v, err=%v", thread.Approvals, err)
	}
	if err := New(dir).ResolveApproval(tid, a.ID, ApprovalDiscarded); err != nil {
		t.Fatal(err)
	}
	if got, _ := New(dir).Approvals(tid); len(got) != 1 || got[0].Status != ApprovalDiscarded || got[0].ResolvedAt.IsZero() {
		t.Fatalf("approvals after resolve = %+v", got)
	}
}

func TestPendingApprovalBlocksFollowup(t *testing.T) {
	s := New(t.TempDir())
	tid, turnID, err := s.ReserveNewThread("stop it", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddApproval(tid, Approval{ID: MintID(), TurnID: turnID, Tool: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReserveFollowup(tid, "what now?", ""); !errors.Is(err, ErrPendingApproval) {
		t.Fatalf("followup error = %v, want pending approval", err)
	}
	approvals, _ := s.Approvals(tid)
	if len(approvals) != 1 || approvals[0].TurnID != turnID {
		t.Fatalf("approvals = %+v, want turn id %q", approvals, turnID)
	}
}

func TestBadIDsAndTitles(t *testing.T) {
	s := New(t.TempDir())
	for _, bad := range []string{"nope", "../x", "", "New chat"} {
		if _, err := s.Get(bad); err == nil {
			t.Fatalf("Get(%q) must fail", bad)
		}
	}
	if err := s.Delete("nope"); err != nil {
		t.Fatal(err)
	}
	if TitleFromPrompt("") != "New chat" {
		t.Fatal("empty title")
	}
	if got := TitleFromPrompt(strings.Repeat("x", 200)); got != strings.Repeat("x", 60)+"\u2026" {
		t.Fatalf("long title = %q", got)
	}
	if _, _, err := s.ReserveNewThread("  ", ""); err == nil {
		t.Fatal("blank prompt must fail")
	}
}
