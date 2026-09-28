package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/agent"
	"pcoder/internal/docker"
	"pcoder/internal/project"
	"pcoder/internal/threads"
)

// butlerTurnFixture builds one envelope turn with butler's payload shape.
func butlerTurnFixture(turnID, prompt, answer string, steps []butlerStep) threads.Turn {
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
		butlerTurnFixture("t1", "brief me", "Healthy.", []butlerStep{{Tool: butlerToolListProjects, Args: "{}", Result: `[{"id":"a/b"}]`}}),
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
// the frontend: fake model at HTTP, mocked engine, real store, real SSE
// framing, real transcript on disk.
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

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK)
	statuses, last := splitSSEBody(t, rec.Body.String())
	if last["answer"] != "One project is healthy; nothing needs attention." {
		t.Fatalf("final = %v", last)
	}
	// SSE order: one line per tool start and finish, terminal done last.
	var seq []string
	for _, s := range statuses {
		tool, _ := s["tool"].(string)
		status, _ := s["status"].(string)
		seq = append(seq, tool+":"+status)
	}
	joined := strings.Join(seq, " ")
	for _, want := range []string{"list_projects:running", "list_projects:done", "events_tail:running", "events_tail:done", "done:answered"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("statuses = %v, want %q in order", seq, want)
		}
	}
	if confs, _ := last["confirms"].([]any); len(confs) != 0 {
		t.Fatalf("confirms = %v, want none on a read-only turn", confs)
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
	tid, _ := last["threadId"].(string)
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
		if len([]rune(s.Result)) > 121 { // summarize cuts at 120 + "…"
			t.Fatalf("step %q result not summarized: %q", s.Tool, s.Result)
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
	rec := butlerPost(t, h, cookie, `{"prompt":"inspect projects and report findings"}`, http.StatusOK)
	_, last := splitSSEBody(t, rec.Body.String())
	if last["answer"] != "All findings are reported." {
		t.Fatalf("final = %v", last)
	}
	if !strings.Contains(fmt.Sprint(secondRound["messages"]), "Inspect projects") {
		t.Fatalf("second round did not receive the first todo state: %v", secondRound["messages"])
	}

	tid, _ := last["threadId"].(string)
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

// CompleteTurn failing mid-stream still answers 200: the failure rides
// the stream with the thread identity for a retry, never a dropped 500.
func TestButlerCompleteTurnFailureStreamsError(t *testing.T) {
	d, _, pinOut, st := newSessionDeps(t)
	butlerDir := t.TempDir()
	d.Butler = threads.New(butlerDir, true)
	f := newFakeModel(t,
		func(w http.ResponseWriter, body map[string]any) {
			// Sabotage the just-reserved thread before answering: the
			// persist below must fail while the answer exists.
			entries, _ := os.ReadDir(butlerDir)
			for _, e := range entries {
				_ = os.RemoveAll(filepath.Join(butlerDir, e.Name()))
			}
			scopeAllow(w, body)
		},
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "answered anyway", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"brief me"}`, http.StatusOK)
	_, last := splitSSEBody(t, rec.Body.String())
	if last["error"] != errInternal {
		t.Fatalf("final = %v, want the transparent error, not the answer", last)
	}
	if last["threadId"] == "" || last["threadTitle"] == "" {
		t.Fatalf("final = %v, want thread identity for a retry", last)
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
		func(w http.ResponseWriter, _ map[string]any) {
			writeCompletion(w, "stop", "What would you like to do next?", nil)
		},
	)
	seedAI(t, st, f.srv.URL)
	h := New(d)
	cookie := loginCookie(t, h, pinOut)

	rec := butlerPost(t, h, cookie, `{"prompt":"stop the api project"}`, http.StatusOK)
	_, last := splitSSEBody(t, rec.Body.String())
	if last["answer"] != "Tap Confirm to stop a/b." {
		t.Fatalf("final = %v", last)
	}
	confs, _ := last["confirms"].([]any)
	if len(confs) != 1 {
		t.Fatalf("confirms = %v, want the stop card", last["confirms"])
	}
	card, _ := confs[0].(map[string]any)
	if card["tool"] != butlerToolStop || card["summary"] == "" || card["blastRadius"] == "" || card["id"] == "" {
		t.Fatalf("card = %v, want tool + summary + blast radius + id", card)
	}
	tid, _ := last["threadId"].(string)
	th, err := d.Butler.Get(butlerScope, tid)
	if err != nil {
		t.Fatal(err)
	}
	var propose butlerPayload
	if len(th.Turns) != 1 {
		t.Fatalf("turns = %+v, want the propose turn", th.Turns)
	}
	_ = json.Unmarshal(th.Turns[0].Payload, &propose)
	if len(propose.Steps) != 1 {
		t.Fatalf("steps = %+v, want the propose step recorded", propose.Steps)
	}
	if !strings.Contains(propose.Steps[0].Result, card["id"].(string)) {
		t.Fatalf("step result = %q, want the confirm id", propose.Steps[0].Result)
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
