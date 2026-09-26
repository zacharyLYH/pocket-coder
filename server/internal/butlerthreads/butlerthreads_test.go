package butlerthreads

import (
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
	if err := s.CompleteTurn(tid, 2, Turn{TurnID: "t2", Prompt: "and now?", Answer: "still fine", Time: th.CreatedAt}); err != nil {
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
