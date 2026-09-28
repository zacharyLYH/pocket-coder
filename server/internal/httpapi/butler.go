// Butler endpoints: one global thread list (no project scope), one turn
// POST running a bounded loop with SSE status lines.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/obs"
	"pcoder/internal/prompt"
	"pcoder/internal/threads"
)

// butlerGuide is the turn system prompt, assembled from the shared prompt
// blocks (see internal/prompt): the tool lists come from the registry
// consts, so a new tool documents itself in the prompt automatically.
var butlerGuide = prompt.ButlerGuide(butlerReadNames, butlerWriteNames)

// butlerBusy serializes one turn globally: a second POST while one is in
// flight gets 409. Taken before the thread is reserved, so a 409 never
// leaves a stray thread behind.
var butlerBusy atomic.Bool

// butlerScope is the thread-store scope for butler: one global history,
// no project prefix.
const butlerScope = ""

func butlerStoreOr500(w http.ResponseWriter, d Deps) (*threads.Store, bool) {
	if d.Butler == nil {
		writeInternalErr(w, "butler store", errors.New("butler store not configured"))
		return nil, false
	}
	return d.Butler, true
}

// sseTurn opens the SSE stream and returns the status and final writers.
// The final line carries the answer on success and a bare-JSON error
// object on failure — the stream is already 200 by then, so failures ride
// the stream too.
func sseTurn(w http.ResponseWriter) (func(tool, msg string), func(v any)) {
	writeSSEHeaders(w)
	return func(tool, msg string) { writeSSEStatus(w, tool, msg) }, func(v any) { writeSSEFinal(w, v) }
}

func handleButlerThreads(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			return
		}
		summaries, err := st.List(butlerScope)
		if err != nil {
			writeInternalErr(w, "list butler threads", err)
			return
		}
		if summaries == nil {
			summaries = []threads.Summary{}
		}
		var running any // no busy-id tracking; remount-into-run is codemap-only
		writeJSON(w, http.StatusOK, map[string]any{"threads": summaries, "runningThreadId": running})
	}
}

func handleButlerThreadGet(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			return
		}
		th, err := st.Get(butlerScope, r.PathValue("tid"))
		if err != nil {
			writeErr(w, http.StatusNotFound, "unknown thread")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread": butlerThreadJSON(th)})
	}
}

func handleButlerThreadDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			return
		}
		tid := r.PathValue("tid")
		// 409 on busy alone: the slot is taken before any thread id is
		// known, so an id comparison would miss the reservation window.
		if butlerBusy.Load() {
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		if err := st.Delete(butlerScope, tid); err != nil {
			writeInternalErr(w, "delete butler thread", err)
			return
		}
		obs.Info(r.Context(), obs.ButlerThreadDeleted, "butler chat "+tid+" deleted", map[string]any{"threadId": tid})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

func butlerThreadJSON(th threads.Thread) any {
	turns := make([]any, 0, len(th.Turns))
	for _, t := range th.Turns {
		var turnErr any
		if t.Error != nil {
			turnErr = *t.Error
		}
		var p butlerPayload
		_ = json.Unmarshal(t.Payload, &p)
		turns = append(turns, map[string]any{
			"turnId": t.TurnID, "prompt": t.Prompt, "answer": p.Answer,
			"steps": p.Steps, "projectHint": t.ProjectHint, "error": turnErr,
			"time": t.Time.Format(time.RFC3339),
		})
	}
	approvals := make([]butlerCard, 0, len(th.Approvals))
	for _, a := range th.Approvals {
		if a.Status != "" && a.Status != threads.ApprovalPending {
			continue
		}
		approvals = append(approvals, butlerCard{ID: a.ID, Tool: a.Tool, Summary: a.Summary, BlastRadius: a.BlastRadius})
	}
	return map[string]any{
		"id": th.ID, "title": th.Title,
		"createdAt": th.CreatedAt.Format(time.RFC3339),
		"updatedAt": th.UpdatedAt.Format(time.RFC3339),
		"turns":     turns,
		"approvals": approvals,
	}
}

// handleButlerRetry reruns the LAST turn of a thread, which must be
// failed. The turn is rewritten in place (same turnId/prompt, fresh time)
// and the pipeline reruns over the same SSE stream as a normal turn.
// Safe for butler's propose-only writes: a replayed intent can only
// produce a confirm card, never a mutation. Blocked while busy or while a
// confirm card is pending.
func handleButlerRetry(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			writeErr(w, http.StatusConflict, "ai not configured")
			return
		}
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			return
		}
		tid := r.PathValue("tid")
		th, gerr := st.Get(butlerScope, tid)
		if gerr != nil {
			writeErr(w, http.StatusNotFound, "unknown thread")
			return
		}
		if len(th.Turns) == 0 || th.Turns[len(th.Turns)-1].Error == nil {
			writeErr(w, http.StatusConflict, "retry only failed turns")
			return
		}
		if !butlerBusy.CompareAndSwap(false, true) {
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		defer func() { butlerBusy.Store(false) }()
		n, turnID, prompt, rerr := st.BeginRetry(butlerScope, tid, "")
		if rerr != nil {
			writeTurnErr(w, http.StatusInternalServerError, "retry reserve: "+rerr.Error(), tid, th.Title)
			return
		}
		runButlerSSE(w, r, d, st, butlerSSETurn{
			threadID: tid, threadTitle: th.Title, turnN: n, turnID: turnID,
			prompt: prompt, projectHint: th.Turns[len(th.Turns)-1].ProjectHint,
		})
	}
}

// butlerSSETurn is one reserved turn ready to stream: the shared back
// half of the turn and retry handlers (reserve differs; everything after
// is identical).
type butlerSSETurn struct {
	threadID, threadTitle, turnID, prompt, projectHint string
	turnN int
}

// runButlerSSE streams one butler turn over SSE and persists it: the
// stream is already 200 before the model runs, so failures ride the
// stream as the final line with thread identity intact (retryable),
// never a dropped 500.
func runButlerSSE(w http.ResponseWriter, r *http.Request, d Deps, st *threads.Store, t butlerSSETurn) {
	cfg := aiConfig(d, aiBody{})
	emit, final := sseTurn(w)
	answer, steps, confirms, lineage, runErr := runButlerTurn(r, d, cfg, st, t.threadID, t.turnID, t.prompt, emit, true, true)
	turn := butlerTurn(t.turnID, t.prompt, t.projectHint, answer, steps)
	if runErr != nil {
		msg := runErr.Error()
		turn.Error = &msg
		_ = st.CompleteTurn(butlerScope, t.threadID, t.turnN, turn, butlerLineage(lineage, t.turnID, t.threadID, t.prompt, msg))
		obsFail(r, obs.ButlerTurn, "butler turn failed", runErr, map[string]any{"threadId": t.threadID})
		final(map[string]any{"error": msg, "threadId": t.threadID, "threadTitle": t.threadTitle})
		return
	}
	if cerr := st.CompleteTurn(butlerScope, t.threadID, t.turnN, turn, butlerLineage(lineage, t.turnID, t.threadID, t.prompt, "")); cerr != nil {
		obsFail(r, obs.ButlerTurn, "complete butler turn failed", cerr, map[string]any{"threadId": t.threadID})
		final(map[string]any{"error": errInternal, "threadId": t.threadID, "threadTitle": t.threadTitle})
		return
	}
	_, _ = d.Events.Append("butler.turn", map[string]any{"threadId": t.threadID})
	final(map[string]any{
		"threadId": t.threadID, "threadTitle": t.threadTitle,
		"turnId": t.turnID, "answer": answer,
		"steps": steps, "confirms": confirms,
		"time": turn.Time.Format(time.RFC3339),
	})
}

func handleButlerTurn(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("ai not configured"), nil)
			writeErr(w, http.StatusConflict, "ai not configured")
			return
		}
		var body struct {
			Prompt      string `json:"prompt"`
			ThreadID    string `json:"threadId"`
			ProjectHint string `json:"projectHint"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.Prompt = strings.TrimSpace(body.Prompt)
		if len(body.Prompt) == 0 || len(body.Prompt) > 2000 {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("prompt must be 1-2000 chars"), nil)
			writeErr(w, http.StatusBadRequest, "prompt must be 1-2000 chars")
			return
		}
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("butler store not configured"), nil)
			return
		}
		if !butlerBusy.CompareAndSwap(false, true) {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("butler busy"), nil)
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		defer func() { butlerBusy.Store(false) }()
		var threadID, turnID, threadTitle string
		var n int
		newThread := body.ThreadID == ""
		if newThread {
			tid, turn, rerr := st.ReserveNewThread(butlerScope, body.Prompt, "", nil)
			if rerr != nil {
				obsFail(r, obs.ButlerTurn, "reserve butler thread failed", rerr, nil)
				writeTurnErr(w, http.StatusInternalServerError, errInternal, "", "")
				return
			}
			threadID, turnID, n = tid, turn, 1
			threadTitle = threads.TitleFromPrompt(body.Prompt)
		} else {
			th, gerr := st.Get(butlerScope, body.ThreadID)
			if gerr != nil {
				writeTurnErr(w, http.StatusNotFound, "unknown thread", "", "")
				return
			}
			threadID, threadTitle = th.ID, th.Title
			var rerr error
			n, turnID, rerr = st.ReserveFollowup(butlerScope, threadID, body.Prompt, "", nil)
			if rerr != nil {
				obsFail(r, obs.ButlerTurn, "reserve butler turn failed", rerr, map[string]any{"threadId": threadID})
				if errors.Is(rerr, threads.ErrPendingApproval) {
					writeTurnErr(w, http.StatusConflict, "pending confirmation — confirm or discard it before continuing", threadID, threadTitle)
					return
				}
				writeTurnErr(w, http.StatusInternalServerError, errInternal, threadID, threadTitle)
				return
			}
		}
		runButlerSSE(w, r, d, st, butlerSSETurn{
			threadID: threadID, threadTitle: threadTitle, turnN: n, turnID: turnID,
			prompt: body.Prompt, projectHint: body.ProjectHint,
		})
	}
}

// butlerScopeParse reads the classifier verdict. False only on a clean
// parse of can_help=false; anything unparseable fails open into the tool
// loop, whose guide still governs.
func butlerScopeParse(raw string) bool {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i > 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var v struct {
		CanHelp *bool `json:"can_help"`
	}
	if err := json.Unmarshal([]byte(s), &v); err != nil || v.CanHelp == nil {
		return true
	}
	return *v.CanHelp
}

// runButlerTurn runs the bounded loop (up to 6 tool steps) with the read
// tools plus propose-only write tools, streaming one SSE status line per
// model round. It returns the final answer, the recorded steps, and the
// confirm cards proposed during the turn. A structured scope check opens
// the turn: out-of-scope asks get the pinned refusal with no tool rounds.
// The gate only ever refutes — any gate failure falls through to the loop.
func runButlerTurn(r *http.Request, d Deps, cfg agent.Config, st *threads.Store, threadID, turnID, userPrompt string, emit func(tool, msg string), checkScope, allowWrites bool) (string, []butlerStep, []butlerCard, *agent.Lineage, error) {
	lineage := &agent.Lineage{}
	var confirms []butlerCard
	var scope *agent.ScopeGate
	if checkScope {
		scope = &agent.ScopeGate{
			Prompt: prompt.ButlerScopePrompt(), SchemaName: "butler_scope", Schema: prompt.ButlerScopeSchema(),
			Refused: func(verdict string) bool { return !butlerScopeParse(verdict) },
			Refusal: prompt.ButlerRefusal,
		}
	}
	tools := append([]agent.Tool{agent.TodoTool(lineage)}, butlerReadTools(d)...)
	if allowWrites {
		tools = append(tools, butlerWriteTools(d, st, threadID, turnID, &confirms)...)
	}
	pipe := agent.Pipeline{
		Scope: scope,
		Context: func(_ string) []map[string]any {
			th, _ := st.Get(butlerScope, threadID)
			return agent.BuildHistory(butlerViews(th), userPrompt, agent.HistoryOpts{MaxTurns: 20, MaxChars: 16 * 1024})
		},
		Run: agent.TurnSpec{
			System: func() string { return butlerGuide },
			Tools:  tools, MaxSteps: 6,
			Opts: []agent.RunOption{agent.WithoutGroundingNudge(), agent.WithLeadIn(prompt.ButlerWorkflows())},
		},
		OnTrace: butlerStream(emit),
	}
	res := pipe.Execute(r.Context(), cfg, lineage, userPrompt)
	if res.Refused {
		emit("done", "answered")
		return res.Answer, []butlerStep{}, []butlerCard{}, res.Lineage, nil
	}
	steps := butlerSteps(res.Rounds)
	if res.Err != nil {
		return "", steps, confirms, res.Lineage, res.Err
	}
	emit("done", "answered")
	return res.Answer, steps, confirms, res.Lineage, nil
}

// butlerStream maps live trace events onto SSE status lines.
func butlerStream(emit func(tool, msg string)) func(agent.TraceEvent) {
	return func(ev agent.TraceEvent) {
		switch ev.Kind {
		case "round":
			emit("model", "running")
		case "tool_start":
			emit(ev.Tool, "running")
		case "tool_done":
			emit(ev.Tool, "done")
		case "model_error":
			emit("model", "error")
		}
	}
}

// butlerStep is butler's persisted step shape (the FE contract).
type butlerStep struct {
	Tool   string `json:"tool"`
	Args   string `json:"args,omitempty"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// butlerPayload is butler's opaque turn payload, stored in the shared
// threads envelope and rebuilt on read.
type butlerPayload struct {
	Answer string        `json:"answer,omitempty"`
	Steps  []butlerStep  `json:"steps,omitempty"`
}

// butlerTurn builds the store envelope for one finished butler turn.
func butlerTurn(turnID, prompt, projectHint, answer string, steps []butlerStep) threads.Turn {
	payload, err := json.Marshal(butlerPayload{Answer: answer, Steps: steps})
	if err != nil {
		payload = nil
	}
	return threads.Turn{TurnID: turnID, Prompt: prompt, ProjectHint: projectHint, Payload: payload, Time: time.Now().UTC()}
}

// butlerSteps maps the transcript to the persisted step shape.
func butlerSteps(rounds []agent.Round) []butlerStep {
	steps := []butlerStep{}
	for _, r := range rounds {
		for _, s := range r.Steps {
			out := butlerStep{Tool: s.Tool, Args: s.Args, Result: summarize(s.Output)}
			if s.Err != "" {
				out.Result, out.Error = "", s.Err
			}
			steps = append(steps, out)
		}
	}
	return steps
}

// butlerViews maps persisted turns onto the shared history view.
func butlerViews(th threads.Thread) []agent.TurnView {
	out := make([]agent.TurnView, 0, len(th.Turns))
	for _, t := range th.Turns {
		var p butlerPayload
		_ = json.Unmarshal(t.Payload, &p)
		v := agent.TurnView{Prompt: t.Prompt, Answer: p.Answer, Failed: t.Error != nil}
		for _, s := range p.Steps {
			v.Steps = append(v.Steps, agent.Step{Tool: s.Tool, Args: s.Args, Output: s.Result, Err: s.Error})
		}
		out = append(out, v)
	}
	return out
}

func runButlerClosure(r *http.Request, d Deps, threadID string, approval threads.Approval) (string, error) {
	st := d.Butler
	if st == nil {
		return "", errors.New("butler store not configured")
	}
	promptText := "The user discarded this proposed action: " + approval.Summary + ". Do not call any write tools. Ask the user what they would like to do next."
	n, turnID, err := st.ReserveFollowup(butlerScope, threadID, promptText, "", nil)
	if err != nil {
		return "", err
	}
	cfg := aiConfig(d, aiBody{})
	emit := func(string, string) {}
	answer, steps, _, lineage, runErr := runButlerTurn(r, d, cfg, st, threadID, turnID, promptText, emit, false, false)
	turn := butlerTurn(turnID, promptText, "", answer, steps)
	if runErr != nil {
		msg := runErr.Error()
		turn.Error = &msg
	}
	if err := st.CompleteTurn(butlerScope, threadID, n, turn, butlerLineage(lineage, turnID, threadID, promptText, turnErrorMessage(turn))); err != nil {
		return "", err
	}
	if runErr != nil {
		return "", runErr
	}
	return answer, nil
}

func turnErrorMessage(turn threads.Turn) string {
	if turn.Error == nil {
		return ""
	}
	return *turn.Error
}

func butlerLineage(l *agent.Lineage, turnID, threadID, prompt, errMsg string) []byte {
	if l == nil {
		l = &agent.Lineage{}
	}
	l.TurnID, l.ThreadID, l.Prompt, l.Time, l.Error = turnID, threadID, prompt, time.Now().UTC(), errMsg
	raw, err := json.Marshal(l)
	if err != nil {
		return nil
	}
	return raw
}

func summarize(s string) string {
	return strings.TrimSpace(cut(s, 120))
}
