package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// scopeAllow scripts the structured scope gate's allow verdict: every turn
// opens with one tools-free classifier call, so each fake script below
// leads with it before the loop's own rounds.
func scopeAllow(w http.ResponseWriter, _ map[string]any) {
	writeCompletion(w, "stop", `{"can_help":true}`, nil)
}

// scopeDeny scripts the gate's refuse verdict.
func scopeDeny(w http.ResponseWriter, _ map[string]any) {
	writeCompletion(w, "stop", `{"can_help":false}`, nil)
}

// butlerPost posts one turn body and pins the expected status.
func butlerPost(t *testing.T, h http.Handler, cookie *http.Cookie, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := authedPost(t, h, cookie, "/api/butler/turn", body)
	if rec.Code != want {
		t.Fatalf("butler turn %s: got %d %q, want %d", body, rec.Code, rec.Body.String(), want)
	}
	return rec
}

// Full turn round-trip with the model faked at HTTP: POST streams SSE
// status lines then the final JSON; the transcript persists globally.
// Framing order is pinned via splitSSEBody (shared contract in sse_test.go).
func TestButlerTurnRoundTrip(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "All three projects are healthy.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me","projectHint":"a/b"}`, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	// Statuses stream in order before the final answer. Zero-tool turns
	// emit only the terminal done line; tool turns bracket with model.
	statuses, last := splitSSEBody(t, rec.Body.String())
	if len(statuses) == 0 || statuses[len(statuses)-1]["tool"] != "done" {
		t.Fatalf("statuses = %v, want terminal done", statuses)
	}
	tid, _ := last["threadId"].(string)
	if tid == "" || last["answer"] != "All three projects are healthy." {
		t.Fatalf("final = %v", last)
	}

	// Transcript: global list + get.
	rec = authedGet(t, h, cookie, "/api/butler/threads")
	var listed struct {
		Threads []struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Threads) != 1 || listed.Threads[0].ID != tid {
		t.Fatalf("listed = %+v, want the new thread %q", listed, tid)
	}
	rec = authedGet(t, h, cookie, "/api/butler/threads/"+tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d %q", rec.Code, rec.Body.String())
	}
	var got struct {
		Thread struct {
			Turns []struct {
				Prompt string `json:"prompt"`
				Answer string `json:"answer"`
				Hint   string `json:"projectHint"`
			} `json:"turns"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Thread.Turns) != 1 || got.Thread.Turns[0].Answer != "All three projects are healthy." {
		t.Fatalf("turns = %+v", got.Thread.Turns)
	}
	if got.Thread.Turns[0].Hint != "a/b" {
		t.Fatalf("hint = %q, want a/b", got.Thread.Turns[0].Hint)
	}

	// Follow-up replays history: the fake sees the prior answer.
	var sawHistory bool
	f2 := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, body map[string]any) {
			raw, _ := json.Marshal(body)
			if strings.Contains(string(raw), "All three projects are healthy.") {
				sawHistory = true
			}
			writeCompletion(w, "stop", "Still healthy.", nil)
		},
	)
	seedAI(t, st, f2.srv.URL)
	butlerPost(t, h, cookie, `{"prompt":"and now?","threadId":"`+tid+`"}`, http.StatusOK)
	if !sawHistory {
		t.Fatal("follow-up did not replay the prior answer")
	}

	// Delete removes the thread.
	rec = authedRequest(t, h, cookie, http.MethodDelete, "/api/butler/threads/"+tid)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: got %d %q", rec.Code, rec.Body.String())
	}
	rec = authedGet(t, h, cookie, "/api/butler/threads/"+tid)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: got %d, want 404", rec.Code)
	}
}

// Second POST while busy gets 409; unknown thread 404s; empty prompt 400s.
func TestButlerTurnGuards(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "hi", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	butlerPost(t, h, cookie, `{"prompt":""}`, http.StatusBadRequest)
	butlerPost(t, h, cookie, `{"prompt":"x","threadId":"abc"}`, http.StatusNotFound)
	if !butlerBusy.CompareAndSwap(false, true) {
		t.Fatal("busy slot not taken")
	}
	defer func() { butlerBusy.Store(false) }()
	butlerPost(t, h, cookie, `{"prompt":"x"}`, http.StatusConflict)
	// Delete 409s on busy alone: the slot is taken before the thread id
	// is known, so an id comparison would miss the reservation window.
	if rec := authedRequest(t, h, cookie, http.MethodDelete, "/api/butler/threads/busy-test"); rec.Code != http.StatusConflict {
		t.Fatalf("delete while busy: got %d, want 409", rec.Code)
	}
}

// No model configured: 409, and the butler routes vanish without a store.
func TestButlerNeedsModel(t *testing.T) {
	d, _, pinOut, _ := newSessionDeps(t)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	if rec := authedPost(t, h, cookie, "/api/butler/turn", `{"prompt":"hi"}`); rec.Code != http.StatusConflict {
		t.Fatalf("no model: got %d, want 409", rec.Code)
	}
}

// A model failure mid-turn still persists the failed turn, then answers
// with one bare-JSON error line carrying threadId + threadTitle: the client
// keeps the reserved turn and can retry. Pinned after a regression where
// the error path vanished behind a second WriteHeader.
func TestButlerTurnFailurePersistsAndStreamsError(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	empty := func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", "", nil) // empty answer → loop error
	}
	// Completion retries an empty answer up to 3 attempts, so script all 3
	// (after the scope gate's allow verdict).
	f := newFakeModel(t, scopeAllow, empty, empty, empty)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK) // failures ride the stream
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q, want text/event-stream", ct)
	}
	statuses, last := splitSSEBody(t, rec.Body.String())
	if len(statuses) == 0 || statuses[len(statuses)-1]["tool"] != "model" || statuses[len(statuses)-1]["status"] != "error" {
		t.Fatalf("statuses = %v, want terminal model:error", statuses)
	}
	tid, _ := last["threadId"].(string)
	if tid == "" {
		t.Fatalf("error final missing threadId: %v", last)
	}
	if _, ok := last["threadTitle"]; !ok {
		t.Fatalf("error final missing threadTitle: %v", last)
	}
	if last["error"] == "" {
		t.Fatalf("error final missing error message: %v", last)
	}

	// The failed turn is on disk with its error, so a retry has history.
	th, gerr := d.Butler.Get(tid)
	if gerr != nil {
		t.Fatalf("failed turn not readable: %v", gerr)
	}
	if len(th.Turns) != 1 || th.Turns[0].Error == nil || th.Turns[0].Answer != "" {
		t.Fatalf("persisted turn = %+v", th.Turns)
	}
	// Single logging is structural now: the deferred ask-log is gone, so
	// each error return below carries its only obsFail. (Project-less
	// butler entries reach slog only — emit drops them from the store by
	// design — so there is no countable assertion here.)
}

// The loop cap: a model that always answers with tool calls (and never a
// final text) still terminates and streams one model status per round.
// The loop must burn its budget and close out — never hang, never loop
// forever.
func TestButlerTurnLoopCapsAtMaxSteps(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	calls := 0
	toolRound := func(w http.ResponseWriter, _ map[string]any) {
		calls++
		writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", butlerToolListProjects, "{}")})
	}
	steps := make([]func(w http.ResponseWriter, _ map[string]any), 0, 8)
	steps = append(steps, scopeAllow) // the gate first, then the 6 capped rounds
	for i := 0; i < 6; i++ {
		steps = append(steps, toolRound)
	}
	steps = append(steps, func(w http.ResponseWriter, _ map[string]any) {
		calls++
		writeCompletion(w, "stop", "gave up", nil) // the close-out call
	})
	f := newFakeModel(t, steps...)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK)
	statuses, last := splitSSEBody(t, rec.Body.String())
	if last["answer"] != "gave up" {
		t.Fatalf("final = %v, want the close-out answer", last)
	}
	// Each executed round streams a model status; the cap bounds tool
	// rounds at 6 (the scope gate is silent and streams nothing).
	modelStatuses := 0
	for _, s := range statuses {
		if s["tool"] == "model" {
			modelStatuses++
		}
	}
	if modelStatuses == 0 || modelStatuses > 7 {
		t.Fatalf("model statuses = %d, want bounded by the 6-step cap", modelStatuses)
	}
	if calls != 7 {
		t.Fatalf("model calls = %d, want 6 capped rounds + 1 close-out (plus the uncounted scope gate)", calls)
	}
}
