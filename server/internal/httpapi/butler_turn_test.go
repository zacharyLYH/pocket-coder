package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/agent"
	"pcoder/internal/docker"
	"pcoder/internal/project"
	"pcoder/internal/threads"
)

// butlerTurnFixture builds one envelope turn with butler's payload shape.
func butlerTurnFixture(turnID, prompt, answer string, steps []agent.Step) threads.Turn {
	payload, _ := json.Marshal(butlerPayload{Answer: answer, Steps: steps})
	return threads.Turn{TurnID: turnID, Prompt: prompt, Payload: payload}
}

// Second-turn context aligns with codemaps: tool steps replay as real
// tool calls (result mapped to output) alongside the answer text, failed
// turns contribute their prompt only, the reserved placeholder is
// dropped, and history is bounded to the last 20 turns.
func TestButlerHistoryReplaysToolSteps(t *testing.T) {
	errMsg := "boom"
	th := threads.Thread{ID: "th", Turns: []threads.Turn{
		butlerTurnFixture("t1", "brief me", "Healthy.", []agent.Step{{Tool: butlerToolListProjects, Args: "{}", Output: `[{"id":"a/b"}]`}}),
		{TurnID: "t2", Prompt: "stop it", Error: &errMsg},
		{TurnID: "t3", Prompt: "and now?"}, // the reserved placeholder
	}}
	h := agent.BuildHistory(butlerViews(th), "and now?", agent.HistoryOpts{MaxTurns: 20, MaxChars: 16 * 1024})
	if len(h) != 3 {
		t.Fatalf("history = %v, want user + assistant(steps) + failed prompt", h)
	}
	if h[0]["role"] != "user" || h[0]["content"] != "brief me" {
		t.Fatalf("h[0] = %v", h[0])
	}
	steps, _ := h[1]["toolSteps"].([]any)
	if h[1]["role"] != "assistant" || h[1]["content"] != "Healthy." || len(steps) != 1 {
		t.Fatalf("h[1] = %v, want answer text plus one replayable step", h[1])
	}
	first, _ := steps[0].(map[string]any)
	if first["tool"] != butlerToolListProjects || first["output"] != `[{"id":"a/b"}]` {
		t.Fatalf("step = %v, want tool + output (mapped from result)", first)
	}
	if h[2]["role"] != "user" || h[2]["content"] != "stop it" {
		t.Fatalf("h[2] = %v, want the failed turn's prompt only", h[2])
	}

	// Bound: 21 turns keep the last 20.
	var many []threads.Turn
	for i := 0; i < 21; i++ {
		many = append(many, butlerTurnFixture(fmt.Sprint(i), fmt.Sprintf("q%d", i), fmt.Sprintf("a%d", i), nil))
	}
	h = agent.BuildHistory(butlerViews(threads.Thread{ID: "th", Turns: many}), "current", agent.HistoryOpts{MaxTurns: 20, MaxChars: 16 * 1024})
	if len(h) != 40 {
		t.Fatalf("bounded history = %d entries, want 20 turns x user+assistant", len(h))
	}
	if h[0]["content"] != "q1" {
		t.Fatalf("h[0] = %v, want the oldest turn dropped", h[0])
	}
}

// Turn-level integration: the full "brief me" flow — scope gate, chained
// reads, summary, persisted transcript. Codemap depth without docker or
// the frontend: fake model at HTTP, mocked engine, real store, plain-JSON
// turn, real transcript on disk.
func TestButlerTurnBriefMeFlow(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git", Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Events.Append("project.ready", map[string]any{"id": "a/b"}); err != nil {
		t.Fatal(err)
	}
	md.EXPECT().Inspect(mock.Anything, "pcoder-a-b").Return(docker.Container{Running: true, Status: "running"}, nil)
	var loopBody map[string]any
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, body map[string]any) {
			loopBody = body // first loop round: guide + workflow second prompt
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", butlerToolListProjects, "{}")})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c2", butlerToolEventsTail, `{"limit":20}`)})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "One project is healthy; nothing needs attention.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	tid := postButlerTurn(t, h, cookie, `{"prompt":"brief me"}`)
	settled := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled); got != "One project is healthy; nothing needs attention." {
		t.Fatalf("settled answer = %q", got)
	}
	// Two prompts then the question: guide, workflow examples, user.
	msgs, _ := loopBody["messages"].([]any)
	if len(msgs) < 3 {
		t.Fatalf("loop messages = %d, want guide + workflows + user", len(msgs))
	}
	msgText := func(i int) string {
		m, _ := msgs[i].(map[string]any)
		c, _ := m["content"].(string)
		return c
	}
	if !strings.Contains(msgText(0), "Read tools (run free") || !strings.Contains(msgText(1), "Typical usage examples") {
		t.Fatalf("loop opens without guide + workflow second prompt")
	}
	if last := msgs[len(msgs)-1]; true {
		m, _ := last.(map[string]any)
		if c, _ := m["content"].(string); c != "brief me" {
			t.Fatalf("loop closes with %q, want the user prompt", cut(c, 60))
		}
	}

	// Transcript: one turn, two recorded steps, redacted results.
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil {
		t.Fatalf("transcript missing: %v", err)
	}
	var transcript butlerPayload
	if len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, want 1 turn", th.Turns)
	}
	_ = json.Unmarshal(th.Turns[0].Payload, &transcript)
	if len(transcript.Steps) != 2 {
		t.Fatalf("steps = %+v, want 2", transcript.Steps)
	}
	if transcript.Steps[0].Tool != butlerToolListProjects || transcript.Steps[1].Tool != butlerToolEventsTail {
		t.Fatalf("steps = %+v", transcript.Steps)
	}
	for _, s := range transcript.Steps {
		if len([]rune(s.Output)) > 121 { // summarize cuts at 120 + "…"
			t.Fatalf("step %q output not summarized: %q", s.Tool, s.Output)
		}
	}
	lineageRaw, err := d.Butler.ReadTurnLineage(butlerScope, tid, 1)
	if err != nil {
		t.Fatalf("lineage missing: %v", err)
	}
	var lineage struct {
		TurnID   string `json:"turnId"`
		ThreadID string `json:"threadId"`
		Prompt   string `json:"prompt"`
		Events   []struct {
			Kind string `json:"kind"`
		} `json:"events"`
	}
	if err := json.Unmarshal(lineageRaw, &lineage); err != nil || lineage.TurnID == "" || lineage.ThreadID != tid || lineage.Prompt != "brief me" || len(lineage.Events) == 0 || lineage.Events[0].Kind != "llm_request" {
		t.Fatalf("lineage = %s, err=%v", lineageRaw, err)
	}
}

// The model owns progress: it creates a checklist, receives the serialized
// list back from the tool, then replaces it with completed items. The final
// state is persisted in lineage, not inferred from the assistant's prose.
func TestButlerTodoLifecyclePersistsCheckedItems(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	var secondRound map[string]any
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("todo-1", "todo", `{"todos":[{"content":"Inspect projects","status":"in_progress","priority":"high"},{"content":"Report findings","status":"pending","priority":"medium"}]}`)})
		},
		func(w http.ResponseWriter, body map[string]any) {
			secondRound = body
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("todo-2", "todo", `{"todos":[{"content":"Inspect projects","status":"completed","priority":"high"},{"content":"Report findings","status":"completed","priority":"medium"}]}`)})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "All findings are reported.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	tid := postButlerTurn(t, h, cookie, `{"prompt":"inspect projects and report findings"}`)
	settled := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled); got != "All findings are reported." {
		t.Fatalf("settled answer = %q", got)
	}
	if !strings.Contains(fmt.Sprint(secondRound["messages"]), "Inspect projects") {
		t.Fatalf("second round did not receive the first todo state: %v", secondRound["messages"])
	}

	raw, err := d.Butler.ReadTurnLineage(butlerScope, tid, 1)
	if err != nil {
		t.Fatal(err)
	}
	var lineage struct {
		Todos []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"todos"`
	}
	if err := json.Unmarshal(raw, &lineage); err != nil {
		t.Fatal(err)
	}
	if len(lineage.Todos) != 2 || lineage.Todos[0].Status != "completed" || lineage.Todos[1].Status != "completed" {
		t.Fatalf("persisted todos = %+v, want both checked off", lineage.Todos)
	}
}

// A whitespace-only prompt is a 400 with no side effects: no thread, no
// model call. The fake has zero scripted calls, so any LLM burn fails.
func TestButlerRejectsBlankPrompt(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"   "}`, http.StatusBadRequest)
	if !strings.Contains(rec.Body.String(), "1-2000") {
		t.Fatalf("body = %q, want the length error", rec.Body.String())
	}
	threads, err := d.Butler.List(butlerScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("threads = %d, want none created", len(threads))
	}
}

// Turn-level integration for writes: the model proposes, the turn carries
// the confirm card, and only the explicit apply runs the docker call.
func TestButlerTurnProposeFlow(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	if err := project.Open(st).Create("a/b", project.Project{Repo: "https://github.com/x/hello.git"}); err != nil {
		t.Fatal(err)
	}
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", butlerToolStop, `{"project":"a/b"}`)})
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "Tap Confirm to stop a/b.", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	tid := postButlerTurn(t, h, cookie, `{"prompt":"stop the api project"}`)
	settled := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled); got != "Tap Confirm to stop a/b." {
		t.Fatalf("settled answer = %q", got)
	}
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil {
		t.Fatal(err)
	}
	var propose butlerPayload
	if len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, want the propose turn", th.Turns)
	}
	if len(th.Approvals) != 1 {
		t.Fatalf("approvals = %+v, want the stop card on the thread", th.Approvals)
	}
	card := map[string]any{"tool": th.Approvals[0].Tool, "summary": th.Approvals[0].Summary, "blastRadius": th.Approvals[0].BlastRadius, "id": th.Approvals[0].ID}
	if card["tool"] != butlerToolStop || card["summary"] == "" || card["blastRadius"] == "" || card["id"] == "" {
		t.Fatalf("card = %v, want tool + summary + blast radius + id", card)
	}
	_ = json.Unmarshal(th.Turns[0].Payload, &propose)
	if len(propose.Steps) != 1 {
		t.Fatalf("steps = %+v, want the propose step recorded", propose.Steps)
	}
	if !strings.Contains(propose.Steps[0].Output, card["id"].(string)) {
		t.Fatalf("step output = %q, want the confirm id", propose.Steps[0].Output)
	}
	blocked := authedPost(t, h, cookie, "/api/butler/turn", `{"prompt":"what now?","threadId":"`+tid+`"}`)
	if blocked.Code != http.StatusConflict || !strings.Contains(blocked.Body.String(), "pending confirmation") {
		t.Fatalf("follow-up while approval pending = %d %q, want conflict", blocked.Code, blocked.Body.String())
	}

	// Nothing ran yet: the docker call is scripted only now, for apply.
	md.EXPECT().Stop(mock.Anything, "pcoder-a-b", mock.Anything).Return(nil)
	applyRec := authedPost(t, h, cookie, "/api/butler/confirms/"+card["id"].(string)+"/apply", `{}`)
	if applyRec.Code != http.StatusOK {
		t.Fatalf("apply: got %d %q", applyRec.Code, applyRec.Body.String())
	}
	var applied struct {
		OK     bool   `json:"ok"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(applyRec.Body.Bytes(), &applied); err != nil || !applied.OK {
		t.Fatalf("apply = %q (err %v)", applyRec.Body.String(), err)
	}
}

// A failed turn carrying a pending card stays locked like any awaiting
// thread: retry must 409 until the card resolves instead of stacking new
// cards onto the locked thread.
func TestButlerRetryBlockedWhileApprovalPending(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	empty := func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", "", nil)
	}
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "tool_calls", "", []map[string]any{toolCall("c1", butlerToolPreviewClose, `{"project":"a/b"}`)})
		},
		empty, empty, empty,
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	tid := postButlerTurn(t, h, cookie, `{"prompt":"close the preview"}`)
	waitThreadSettled(t, h, cookie, tid) // settles as awaiting (pending card)
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil || len(th.Turns) != 1 || th.Turns[0].Error == nil {
		t.Fatalf("turns = %+v, want one failed turn", th.Turns)
	}
	pending := 0
	for _, a := range th.Approvals {
		if a.Status == "" || a.Status == threads.ApprovalPending {
			pending++
		}
	}
	if pending != 1 {
		t.Fatalf("approvals = %+v, want one pending card", th.Approvals)
	}

	rec := authedPost(t, h, cookie, "/api/butler/threads/"+tid+"/retry", `{}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pending confirmation") {
		t.Fatalf("retry with pending card = %d %q, want 409", rec.Code, rec.Body.String())
	}
	th, _ = d.Butler.Get(butlerScope, tid)
	if len(th.Turns) != 1 {
		t.Fatalf("turns = %d, want still 1 (no rerun started)", len(th.Turns))
	}
}

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

// codeAllow scripts the codemap gate's allow verdict, same contract as
// scopeAllow: the classifier call precedes the loop's rounds.
func codeAllow(w http.ResponseWriter, _ map[string]any) {
	writeCompletion(w, "stop", `{"about_code":true}`, nil)
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

// Full turn round-trip with the model faked at HTTP: POST answers plain
// JSON; the transcript persists globally.
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

	tid := postButlerTurn(t, h, cookie, `{"prompt":"brief me","projectHint":"a/b"}`)
	settled := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled); got != "All three projects are healthy." {
		t.Fatalf("settled answer = %q, want the model answer", got)
	}

	// Transcript: global list + get.
	rec := authedGet(t, h, cookie, "/api/butler/threads")
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
	postButlerTurn(t, h, cookie, `{"prompt":"and now?","threadId":"`+tid+`"}`)
	waitThreadSettled(t, h, cookie, tid)
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
	if !butlerRuns.take(butlerRunKey, "busy-test") {
		t.Fatal("busy slot not taken")
	}
	defer func() { butlerRuns.done(butlerRunKey) }()
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
// with a JSON error carrying threadId + threadTitle: the client keeps
// the reserved turn and can retry.
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

	tid := postButlerTurn(t, h, cookie, `{"prompt":"brief me"}`)
	// The model fails in the background; the reserved turn settles as failed
	// rather than the POST carrying the error.
	settled := waitThreadSettled(t, h, cookie, tid)
	if status, _ := settled["status"].(string); status != "failed" {
		t.Fatalf("settled status = %q, want failed", status)
	}

	// The failed turn is on disk with its error, so a retry has history.
	th, gerr := d.Butler.Get(butlerScope, tid)
	if gerr != nil {
		t.Fatalf("failed turn not readable: %v", gerr)
	}
	if len(th.Turns) != 1 || th.Turns[0].Error == nil {
		t.Fatalf("persisted turn = %+v", th.Turns)
	}
	if lineage, err := d.Butler.ReadTurnLineage(butlerScope, tid, 1); err != nil || !strings.Contains(string(lineage), `"error"`) {
		t.Fatalf("failed-turn lineage = %s, err=%v", lineage, err)
	}

	// Retry reruns the failed turn in place: same turnId, fresh answer.
	// The retry stream opens with the scope gate again.
	f2 := newFakeModel(t, scopeAllow, func(w http.ResponseWriter, _ map[string]any) {
		writeCompletion(w, "stop", "Recovered answer.", nil)
	})
	seedAI(t, st, f2.srv.URL)
	rec3 := authedPost(t, h, cookie, "/api/butler/threads/"+tid+"/retry", `{}`)
	if rec3.Code != http.StatusOK {
		t.Fatalf("retry: got %d %q", rec3.Code, rec3.Body)
	}
	settled2 := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled2); got != "Recovered answer." {
		t.Fatalf("retry answer = %q, want the recovered answer", got)
	}
	th2, gerr := d.Butler.Get(butlerScope, tid)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if len(th2.Turns) != 1 || th2.Turns[0].Error != nil {
		t.Fatalf("after retry turns = %+v, want 1 rewritten, error cleared", th2.Turns)
	}
	// Retrying a successful thread 409s.
	rec4 := authedPost(t, h, cookie, "/api/butler/threads/"+tid+"/retry", `{}`)
	if rec4.Code != http.StatusConflict {
		t.Fatalf("retry success: got %d %q, want 409", rec4.Code, rec4.Body)
	}
	// each error return below carries its only obsFail. (Project-less
	// butler entries reach slog only — emit drops them from the store by
	// design — so there is no countable assertion here.)
}

// The loop cap: a model that always answers with tool calls (and never a
// final text) still terminates. The loop must burn its budget and close
// out — never hang, never loop forever.
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

	tid := postButlerTurn(t, h, cookie, `{"prompt":"brief me"}`)
	settled := waitThreadSettled(t, h, cookie, tid)
	if got := settledTurnAnswer(t, settled); got != "gave up" {
		t.Fatalf("settled answer = %q, want the close-out answer", got)
	}
	if calls != 7 {
		t.Fatalf("model calls = %d, want 6 capped rounds + 1 close-out (plus the uncounted scope gate)", calls)
	}
}

func TestRunTrackerSemantics(t *testing.T) {
	tr := newRunTracker()
	if _, busy := tr.running("a"); busy {
		t.Fatal("fresh tracker reports busy")
	}
	if !tr.take("a", "") {
		t.Fatal("first take must hold")
	}
	if tr.take("a", "t2") {
		t.Fatal("second take must 409")
	}
	// Empty id while taken-before-reserve still counts as busy.
	if tid, busy := tr.running("a"); !busy || tid != "" {
		t.Fatalf("running = %q,%v, want '',true", tid, busy)
	}
	tr.set("a", "t1")
	if tid, busy := tr.running("a"); !busy || tid != "t1" {
		t.Fatalf("running = %q,%v, want 't1',true", tid, busy)
	}
	// Set on an unheld key is a no-op (never resurrect a released slot).
	tr.done("a")
	tr.set("a", "t9")
	if _, busy := tr.running("a"); busy {
		t.Fatal("set after done must stay released")
	}
	// Keys are independent (codemap per-project vs butler global).
	if !tr.take("a", "x") || !tr.take("b", "y") {
		t.Fatal("independent keys must both hold")
	}
}

// The client-visible turn-error contract is one shape: {error} plus
// thread identity when one exists. Reserve failures use it too — no
// bespoke stage field for clients to branch on.
func TestWriteTurnErrShape(t *testing.T) {
	rec := httptest.NewRecorder()
	writeTurnErr(rec, http.StatusInternalServerError, "create thread: boom", "", "")
	var bare map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &bare); err != nil {
		t.Fatal(err)
	}
	if bare["error"] != "create thread: boom" || len(bare) != 1 {
		t.Fatalf("reserve failure shape = %v, want error only", bare)
	}
	rec = httptest.NewRecorder()
	writeTurnErr(rec, http.StatusConflict, "busy", "tid", "title")
	var full map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatal(err)
	}
	if full["error"] != "busy" || full["threadId"] != "tid" || full["threadTitle"] != "title" || len(full) != 3 {
		t.Fatalf("identified shape = %v, want error+threadId+threadTitle", full)
	}
}

// Prompt limits count runes, not bytes: 2000 emoji (8000 bytes) are a
// legal prompt; 2001 ASCII chars are not.
func TestButlerPromptRuneLimit(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "noted", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	butlerPost(t, h, cookie, `{"prompt":"`+strings.Repeat("x", 2001)+`"}`, http.StatusBadRequest)
	butlerPost(t, h, cookie, `{"prompt":"`+strings.Repeat("🙂", 2001)+`"}`, http.StatusBadRequest)
	// 2000 runes over 2000 bytes would 400 on a byte count; must run.
	tid := postButlerTurn(t, h, cookie, `{"prompt":"`+strings.Repeat("🙂", 2000)+`"}`)
	if got := settledTurnAnswer(t, waitThreadSettled(t, h, cookie, tid)); got != "noted" {
		t.Fatalf("rune prompt answer = %q", got)
	}
}

// GET threads reports the in-flight thread as a running row while a turn
// runs, and ready once it completes — the remount path the sheet relies on.
func TestButlerThreadsReportsRunningThread(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	release := make(chan struct{})
	var once sync.Once
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			<-release
			writeCompletion(w, "stop", "done", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- authedPost(t, h, cookie, "/api/butler/turn", `{"prompt":"hi"}`)
	}()
	var tid string
	for i := 0; i < 200; i++ {
		rec := authedGet(t, h, cookie, "/api/butler/threads")
		var body struct {
			Threads []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"threads"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Threads) == 1 && body.Threads[0].Status == "running" {
			tid = body.Threads[0].ID
			break
		}
		select {
		case rec := <-done:
			t.Fatalf("turn finished early: %d", rec.Code)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if tid == "" {
		once.Do(func() { close(release) })
		t.Fatal("running thread never appeared in list")
	}
	once.Do(func() { close(release) })
	if rec := <-done; rec.Code != http.StatusOK {
		t.Fatalf("turn: got %d %q, want 200", rec.Code, rec.Body.String())
	}
	waitThreadSettled(t, h, cookie, tid)
	rec := authedGet(t, h, cookie, "/api/butler/threads")
	var body struct {
		Threads []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Threads) != 1 || body.Threads[0].Status != "ready" {
		t.Fatalf("rows after completion = %+v, want one ready row", body.Threads)
	}
}

// butler.turn audits the completed turn like codemap.turn does: thread,
// turn, prompt excerpt, and step count — not just the thread id.
func TestButlerTurnAuditEvent(t *testing.T) {
	d, md, pinOut, st := newSessionDeps(t)
	_ = md
	f := newFakeModel(t, scopeAllow,
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "ok", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)
	rec := butlerPost(t, h, cookie, `{"prompt":"audit me"}`, http.StatusOK)
	last := finalBody(t, rec)
	tid, _ := last["threadId"].(string)
	turnID, _ := last["turnId"].(string)
	// The audit event is appended by the detached run after completion.
	waitThreadSettled(t, h, cookie, tid)

	evs, err := d.Events.Read(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range evs {
		if ev.Type != "butler.turn" {
			continue
		}
		found = true
		if ev.Data["threadId"] != tid || ev.Data["turnId"] != turnID {
			t.Fatalf("audit identity = %v, want %s/%s", ev.Data, tid, turnID)
		}
		if p, _ := ev.Data["prompt"].(string); !strings.Contains(p, "audit me") {
			t.Fatalf("audit prompt = %v, want excerpt", ev.Data)
		}
		if n, _ := ev.Data["steps"].(float64); n != 0 {
			t.Fatalf("audit steps = %v, want 0 on a tools-free turn", ev.Data)
		}
	}
	if !found {
		t.Fatal("no butler.turn audit event")
	}
}

// finalBody parses a plain-JSON turn response into its final object.
// Replaces the old SSE splitSSEBody contract: no status lines exist.
func finalBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var last map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
		t.Fatalf("turn body not JSON: %v %q", err, rec.Body.String())
	}
	return last
}

// postButlerTurn posts one turn and returns the reserved thread id. Turns
// now run in a background goroutine and the POST answers as soon as the
// placeholder is persisted, so callers must waitThreadSettled before
// asserting on the transcript or starting another run.
func postButlerTurn(t *testing.T, h http.Handler, cookie *http.Cookie, body string) string {
	t.Helper()
	rec := butlerPost(t, h, cookie, body, http.StatusOK)
	tid, _ := finalBody(t, rec)["threadId"].(string)
	if tid == "" {
		t.Fatalf("reserve response missing threadId: %s", rec.Body.String())
	}
	return tid
}

// waitThreadSettled polls the butler thread GET until its derived status
// leaves "running" — i.e. the detached agent goroutine finished and
// released the global run slot. Returns the thread's GET object.
func waitThreadSettled(t *testing.T, h http.Handler, cookie *http.Cookie, tid string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		rec := authedGet(t, h, cookie, "/api/butler/threads/"+tid)
		var body struct {
			Thread map[string]any `json:"thread"`
		}
		if rec.Code == http.StatusOK {
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err == nil {
				if status, _ := body.Thread["status"].(string); status != "running" {
					// status leaves running before the deferred done() runs;
					// wait for the slot too so the next test's take holds.
					if _, busy := butlerRuns.running(butlerRunKey); !busy {
						return body.Thread
					}
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("butler thread %s never settled; last=%s", tid, rec.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// settledTurnAnswer reads the last turn's answer once the thread settles.
func settledTurnAnswer(t *testing.T, th map[string]any) string {
	t.Helper()
	turns, _ := th["turns"].([]any)
	if len(turns) == 0 {
		return ""
	}
	last, _ := turns[len(turns)-1].(map[string]any)
	answer, _ := last["answer"].(string)
	return answer
}
