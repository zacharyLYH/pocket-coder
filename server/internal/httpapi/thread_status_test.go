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

func TestRunningForExactMatchDuringReserve(t *testing.T) {
	// take(key, "") is the reserve window: the thread id is not known
	// yet. It must not report unrelated threads as running, and must
	// not block their deletes — the slot still 409s new turns via take.
	tr := newRunTracker()
	if !tr.take("k", "") {
		t.Fatal("first take must hold")
	}
	defer tr.done("k")
	if runningFor(tr, "k", "t1") {
		t.Fatal("empty running id must not match unrelated thread t1")
	}
	if deleteBlocked(tr, "k", "t1") {
		t.Fatal("reserve window must not block delete of unrelated thread t1")
	}
	tr.set("k", "t1")
	if !runningFor(tr, "k", "t1") {
		t.Fatal("repointed running id must match t1")
	}
	if runningFor(tr, "k", "t2") {
		t.Fatal("repointed running id must not match unrelated thread t2")
	}
	if !deleteBlocked(tr, "k", "t1") {
		t.Fatal("in-flight thread t1 must stay delete-blocked")
	}
	if deleteBlocked(tr, "k", "t2") {
		t.Fatal("unrelated thread t2 must stay deletable during t1's run")
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
