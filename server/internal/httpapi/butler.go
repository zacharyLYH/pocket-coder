// Butler endpoints: one global thread list (no project scope). A turn is
// reserved synchronously (the prompt lands on disk before the POST
// returns) while the agent loop runs detached, so the client polls the
// thread from "running" to its answer.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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

// butlerRuns serializes one turn globally (butlerRunKey): a second POST
// while one is in flight gets 409. Taken before the thread is reserved,
// so a 409 never leaves a stray thread behind; repointed at the thread id
// after reserve so per-row status reports the live run as running.
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

// handleButlerThreads lists the global chats with per-thread status.

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
		rows := statusRows(summaries, func(id string) string {
			th, gerr := st.Get(butlerScope, id)
			if gerr != nil {
				return "ready"
			}
			return butlerStatus(th)
		})
		writeJSON(w, http.StatusOK, map[string]any{"threads": rows})
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
		writeJSON(w, http.StatusOK, map[string]any{"thread": butlerThreadJSON(th, butlerStatus(th))})
	}
}

func handleButlerThreadDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			return
		}
		tid := r.PathValue("tid")
		// Block only the in-flight thread: deleting an unrelated chat
		// during a run stays allowed.
		if deleteBlocked(butlerRuns, butlerRunKey, tid) {
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

func butlerThreadJSON(th threads.Thread, status string) any {
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
		"status":    status,
	}
}

// handleButlerRetry reruns the LAST turn of a thread, which must be
// failed. The turn is rewritten in place (same turnId/prompt, fresh time).
// Safe for butler's propose-only writes: a replayed intent can only
// produce a confirm card, never a mutation.
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
		// A pending card locks the thread like any awaiting state: retry
		// must not stack new cards onto it.
		if pendingApprovals(th) {
			writeTurnErr(w, http.StatusConflict, "pending confirmation — confirm or discard it before continuing", tid, th.Title)
			return
		}
		if !butlerRuns.take(butlerRunKey, "") {
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		n, turnID, prompt, rerr := st.BeginRetry(butlerScope, tid, "")
		if rerr != nil {
			// Release on the failed reserve here; the detached run owns
			// the slot once it is handed the reserved turn.
			butlerRuns.done(butlerRunKey)
			writeTurnErr(w, http.StatusInternalServerError, "retry reserve: "+rerr.Error(), tid, th.Title)
			return
		}
		butlerRuns.set(butlerRunKey, tid)
		startButlerTurn(w, r, d, st, reservedTurn{
			threadID: tid, turnID: turnID, title: th.Title, n: n,
		}, prompt, th.Turns[len(th.Turns)-1].ProjectHint)
	}
}

// handleButlerTurn reserves the turn (persisting the user's prompt
// immediately), starts the agent loop in a detached goroutine, and
// returns 200 with the thread identity right away. The client polls the
// thread to see the user's bubble, the running status, then the answer.
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
		if len([]rune(body.Prompt)) == 0 || len([]rune(body.Prompt)) > 2000 {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("prompt must be 1-2000 chars"), nil)
			writeErr(w, http.StatusBadRequest, "prompt must be 1-2000 chars")
			return
		}
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("butler store not configured"), nil)
			return
		}
		if !butlerRuns.take(butlerRunKey, "") {
			obsFail(r, obs.ButlerAsk, "butler ask failed", errors.New("butler busy"), nil)
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		rt, rerr := reserveTurn(st, butlerScope, body.ThreadID, body.Prompt, "")
		if rerr != nil {
			butlerRuns.done(butlerRunKey)
			obsFail(r, obs.ButlerTurn, "reserve butler turn failed", rerr, nil)
			if errors.Is(rerr, threads.ErrPendingApproval) {
				th, _ := st.Get(butlerScope, body.ThreadID)
				title := ""
				if th.ID != "" {
					title = th.Title
				}
				writeTurnErr(w, http.StatusConflict, "pending confirmation — confirm or discard it before continuing", body.ThreadID, title)
				return
			}
			if body.ThreadID != "" {
				writeTurnErr(w, http.StatusNotFound, "unknown thread", "", "")
				return
			}
			writeTurnErr(w, http.StatusInternalServerError, errInternal, "", "")
			return
		}
		butlerRuns.set(butlerRunKey, rt.threadID)
		startButlerTurn(w, r, d, st, rt, body.Prompt, body.ProjectHint)
	}
}

// startButlerTurn detaches the agent loop from the request (a refresh
// never cancels it) and answers with the reservation identity the client
// polls from. The run owns the global slot until it completes.
func startButlerTurn(w http.ResponseWriter, r *http.Request, d Deps, st *threads.Store, t reservedTurn, prompt, projectHint string) {
	go executeButlerTurn(detached(r), d, st, t, prompt, projectHint)
	writeJSON(w, http.StatusOK, map[string]any{
		"threadId": t.threadID, "threadTitle": t.title,
		"turnId": t.turnID, "time": time.Now().UTC().Format(time.RFC3339),
	})
}

// executeButlerTurn runs one reserved turn detached from the HTTP request
// (refresh never cancels it). The placeholder was reserved before the
// model ran, so failures persist as retryable turns. Completion releases
// the global slot via the deferred done.
func executeButlerTurn(r *http.Request, d Deps, st *threads.Store, t reservedTurn, prompt, projectHint string) {
	defer butlerRuns.done(butlerRunKey)
	cfg := aiConfig(d, aiBody{})
	answer, steps, lineage, runErr := runButlerTurn(r, d, cfg, st, t.threadID, t.turnID, prompt)
	turn := butlerTurn(t.turnID, prompt, projectHint, answer, steps)
	if runErr != nil {
		msg := runErr.Error()
		turn.Error = &msg
		_ = st.CompleteTurn(butlerScope, t.threadID, t.n, turn, butlerLineage(lineage, t.turnID, t.threadID, prompt, msg))
		obsFail(r, obs.ButlerTurn, "butler turn failed", runErr, map[string]any{"threadId": t.threadID})
		return
	}
	if cerr := st.CompleteTurn(butlerScope, t.threadID, t.n, turn, butlerLineage(lineage, t.turnID, t.threadID, prompt, "")); cerr != nil {
		obsFail(r, obs.ButlerTurn, "complete butler turn failed", cerr, map[string]any{"threadId": t.threadID})
		return
	}
	_, _ = d.Events.Append("butler.turn", map[string]any{
		"threadId": t.threadID, "turnId": t.turnID,
		"prompt": capData(prompt, 500), "steps": len(steps),
	})
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
// tools plus propose-only write tools. Cards proposed mid-turn persist
// straight to the thread's approvals; the turn response carries only the
// answer. A structured scope check opens the turn: out-of-scope asks get
// the pinned refusal with no tool rounds. The gate only ever refutes —
// any gate failure falls through to the loop.
func runButlerTurn(r *http.Request, d Deps, cfg agent.Config, st *threads.Store, threadID, turnID, userPrompt string) (string, []agent.Step, *agent.Lineage, error) {
	lineage := &agent.Lineage{}
	scope := &agent.ScopeGate{
		Prompt: prompt.ButlerScopePrompt(), SchemaName: "butler_scope", Schema: prompt.ButlerScopeSchema(),
		Refused: func(verdict string) bool { return !butlerScopeParse(verdict) },
		Refusal: prompt.ButlerRefusal,
	}
	tools := append([]agent.Tool{agent.TodoTool(lineage)}, butlerReadTools(d)...)
	tools = append(tools, butlerWriteTools(d, st, threadID, turnID)...)
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
	}
	res := pipe.Execute(r.Context(), cfg, lineage, userPrompt)
	if res.Refused {
		return res.Answer, []agent.Step{}, res.Lineage, nil
	}
	steps := butlerSteps(res.Rounds)
	if res.Err != nil {
		return "", steps, res.Lineage, res.Err
	}
	return res.Answer, steps, res.Lineage, nil
}

// butlerPayload is butler's opaque turn payload, stored in the shared
// threads envelope and rebuilt on read. Steps ARE agent.Step (the one
// shared shape both products persist); butler just summarizes outputs
// shorter than codemap does.
type butlerPayload struct {
	Answer string       `json:"answer,omitempty"`
	Steps  []agent.Step `json:"steps,omitempty"`
}

// butlerTurn builds the store envelope for one finished butler turn.
func butlerTurn(turnID, prompt, projectHint, answer string, steps []agent.Step) threads.Turn {
	payload, err := json.Marshal(butlerPayload{Answer: answer, Steps: steps})
	if err != nil {
		payload = nil
	}
	return threads.Turn{TurnID: turnID, Prompt: prompt, ProjectHint: projectHint, Payload: payload, Time: time.Now().UTC()}
}

// butlerSteps maps the transcript to persisted steps: same shape as
// codemap, outputs summarized to one line.
func butlerSteps(rounds []agent.Round) []agent.Step {
	steps := []agent.Step{}
	for _, r := range rounds {
		for _, s := range r.Steps {
			out := agent.Step{Tool: s.Tool, Args: s.Args, Output: summarize(s.Output)}
			if s.Err != "" {
				out.Output, out.Err = "", s.Err
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
		v.Steps = append(v.Steps, p.Steps...)
		out = append(out, v)
	}
	return out
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
