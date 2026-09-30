package httpapi

import (
	"testing"

	"pcoder/internal/threads"
)

func TestThreadStatusOrder(t *testing.T) {
	if got := threadStatus(true, true, true); got != "running" {
		t.Fatalf("running+awaiting+failed = %q, want running", got)
	}
	if got := threadStatus(false, true, true); got != "awaiting" {
		t.Fatalf("awaiting+failed = %q, want awaiting", got)
	}
	if got := threadStatus(false, false, true); got != "failed" {
		t.Fatalf("failed = %q", got)
	}
	if got := threadStatus(false, false, false); got != "ready" {
		t.Fatalf("idle = %q", got)
	}
}

func TestButlerStatusDerivation(t *testing.T) {
	th := threads.Thread{ID: "t1", Turns: []threads.Turn{{TurnID: "a"}}}
	if got := butlerStatus(th); got != "ready" {
		t.Fatalf("idle = %q, want ready", got)
	}
	msg := "boom"
	th.Turns[0].Error = &msg
	if got := butlerStatus(th); got != "failed" {
		t.Fatalf("errored = %q, want failed", got)
	}
	th.Turns[0].Error = nil
	th.Approvals = []threads.Approval{{ID: "c1", Status: threads.ApprovalPending}}
	if got := butlerStatus(th); got != "awaiting" {
		t.Fatalf("pending card = %q, want awaiting", got)
	}
	if !butlerRuns.take(butlerRunKey, th.ID) {
		t.Fatal("take must hold")
	}
	defer butlerRuns.done(butlerRunKey)
	if got := butlerStatus(th); got != "running" {
		t.Fatalf("tracked = %q, want running", got)
	}
	if got := codemapStatus("p", threads.Thread{ID: "t1", Approvals: th.Approvals}); got != "ready" {
		t.Fatalf("codemap ignores cards: %q, want ready", got)
	}
}
