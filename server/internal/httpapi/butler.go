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
	"pcoder/internal/butlerthreads"
	"pcoder/internal/obs"
	"pcoder/internal/prompt"
)

// butlerGuide is the turn system prompt, assembled from the shared prompt
// blocks (see internal/prompt): the tool lists come from the registry
// consts, so a new tool documents itself in the prompt automatically.
var butlerGuide = prompt.ButlerGuide(butlerReadNames, butlerWriteNames)

// butlerBusy serializes one turn globally: a second POST while one is in
// flight gets 409. Taken before the thread is reserved, so a 409 never
// leaves a stray thread behind.
var butlerBusy atomic.Bool

func butlerStoreOr500(w http.ResponseWriter, d Deps) (*butlerthreads.Store, bool) {
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
		summaries, err := st.List()
		if err != nil {
			writeInternalErr(w, "list butler threads", err)
			return
		}
		if summaries == nil {
			summaries = []butlerthreads.Summary{}
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
		th, err := st.Get(r.PathValue("tid"))
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
		if err := st.Delete(tid); err != nil {
			writeInternalErr(w, "delete butler thread", err)
			return
		}
		obs.Info(r.Context(), obs.ButlerThreadDeleted, "butler chat "+tid+" deleted", map[string]any{"threadId": tid})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

func butlerThreadJSON(th butlerthreads.Thread) any {
	turns := make([]any, 0, len(th.Turns))
	for _, t := range th.Turns {
		var turnErr any
		if t.Error != nil {
			turnErr = *t.Error
		}
		turns = append(turns, map[string]any{
			"turnId": t.TurnID, "prompt": t.Prompt, "answer": t.Answer,
			"steps": t.Steps, "projectHint": t.ProjectHint, "error": turnErr,
			"time": t.Time.Format(time.RFC3339),
		})
	}
	return map[string]any{
		"id": th.ID, "title": th.Title,
		"createdAt": th.CreatedAt.Format(time.RFC3339),
		"updatedAt": th.UpdatedAt.Format(time.RFC3339),
		"turns":     turns,
	}
}

func handleButlerTurn(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ButlerAsk, "butler ask failed", err, nil)
			}
		}()
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			err = errors.New("ai not configured")
			writeErr(w, http.StatusConflict, err.Error())
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
		if len(body.Prompt) == 0 || len(body.Prompt) > 2000 {
			err = errors.New("prompt must be 1-2000 chars")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		st, ok := butlerStoreOr500(w, d)
		if !ok {
			err = errors.New("butler store not configured")
			return
		}
		if !butlerBusy.CompareAndSwap(false, true) {
			err = errors.New("butler busy")
			writeErr(w, http.StatusConflict, "butler busy — wait for the current run")
			return
		}
		defer func() { butlerBusy.Store(false) }()
		var threadID, turnID, threadTitle string
		var n int
		newThread := body.ThreadID == ""
		if newThread {
			tid, turn, rerr := st.ReserveNewThread(body.Prompt, body.ProjectHint)
			if rerr != nil {
				err = rerr
				obsFail(r, obs.ButlerTurn, "reserve butler thread failed", rerr, nil)
				writeTurnErr(w, http.StatusInternalServerError, errInternal, "", "")
				return
			}
			threadID, turnID, n = tid, turn, 1
			threadTitle = butlerthreads.TitleFromPrompt(body.Prompt)
		} else {
			th, gerr := st.Get(body.ThreadID)
			if gerr != nil {
				writeTurnErr(w, http.StatusNotFound, "unknown thread", "", "")
				return
			}
			threadID, threadTitle = th.ID, th.Title
			var rerr error
			n, turnID, rerr = st.ReserveFollowup(threadID, body.Prompt, body.ProjectHint)
			if rerr != nil {
				err = rerr
				obsFail(r, obs.ButlerTurn, "reserve butler turn failed", rerr, map[string]any{"threadId": threadID})
				writeTurnErr(w, http.StatusInternalServerError, errInternal, threadID, threadTitle)
				return
			}
		}
		emit, final := sseTurn(w)
		answer, steps, confirms, runErr := runButlerTurn(r, d, cfg, st, threadID, body.Prompt, emit)
		turn := butlerthreads.Turn{TurnID: turnID, Prompt: body.Prompt, Answer: answer,
			Steps: steps, ProjectHint: body.ProjectHint, Time: time.Now().UTC(),
		}
		if runErr != nil {
			msg := runErr.Error()
			turn.Error = &msg
			_ = st.CompleteTurn(threadID, n, turn)
			err = runErr
			obsFail(r, obs.ButlerTurn, "butler turn failed", runErr, map[string]any{"threadId": threadID})
			// The stream is already 200: a second WriteHeader would be dropped
			// and the client would read a bare error as the answer. Failures
			// ride the stream as the final line, identity intact.
			final(map[string]any{"error": msg, "threadId": threadID, "threadTitle": threadTitle})
			return
		}
		if cerr := st.CompleteTurn(threadID, n, turn); cerr != nil {
			err = cerr
			obsFail(r, obs.ButlerTurn, "complete butler turn failed", cerr, map[string]any{"threadId": threadID})
			writeTurnErr(w, http.StatusInternalServerError, errInternal, threadID, threadTitle)
			return
		}
		_, _ = d.Events.Append("butler.turn", map[string]any{"threadId": threadID})
		final(map[string]any{
			"threadId": threadID, "threadTitle": threadTitle,
			"turnId": turnID, "answer": answer,
			"steps": turn.Steps, "confirms": confirms,
			"time": turn.Time.Format(time.RFC3339),
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
func runButlerTurn(r *http.Request, d Deps, cfg agent.Config, st *butlerthreads.Store, threadID, userPrompt string, emit func(tool, msg string)) (string, []butlerthreads.Step, []butlerCard, error) {
	if scoped, serr := agent.Structured(r.Context(), cfg, prompt.ButlerScopePrompt(), userPrompt, "butler_scope", prompt.ButlerScopeSchema()); serr == nil && !butlerScopeParse(scoped) {
		emit("done", "answered")
		return prompt.ButlerRefusal, []butlerthreads.Step{}, []butlerCard{}, nil
	}
	th, _ := st.Get(threadID)
	history := butlerHistory(th, userPrompt)
	var steps []butlerthreads.Step
	onTrace := func(ev agent.TraceEvent) {
		switch ev.Kind {
		case "round":
			emit("model", "running")
		case "tool_start":
			emit(ev.Tool, "running")
		case "tool_done":
			step := butlerthreads.Step{Tool: ev.Tool, Args: ev.Args, Result: summarize(ev.Result)}
			if ev.Err != "" {
				step.Result, step.Error = "", ev.Err
			}
			steps = append(steps, step)
			emit(ev.Tool, "done")
		case "model_error":
			emit("model", "error")
		}
	}
	var confirms []butlerCard
	tools := append(butlerReadTools(d), butlerWriteTools(d, &confirms)...)
	answer, err := agent.Run(r.Context(), cfg, butlerGuide, userPrompt, history, tools, "", nil, 6, onTrace, nil,
		agent.WithoutGroundingNudge(), agent.WithLeadIn(prompt.ButlerWorkflows()))
	if err != nil {
		return "", steps, confirms, err
	}
	if steps == nil {
		steps = []butlerthreads.Step{}
	}
	if confirms == nil {
		confirms = []butlerCard{}
	}
	emit("done", "answered")
	return answer, steps, confirms, nil
}

func summarize(s string) string {
	return strings.TrimSpace(cut(s, 120))
}

// butlerHistory rebuilds the LLM conversation from persisted turns, the
// way codemap's threadHistory does: each turn becomes user(prompt) plus
// one assistant entry carrying the tool steps and the answer text. Failed
// turns contribute their prompt only. Context is bounded to the last 20
// turns and ~16KB estimated chars.
func butlerHistory(th butlerthreads.Thread, userPrompt string) []map[string]any {
	turns := th.Turns
	if len(turns) > 20 {
		turns = turns[len(turns)-20:]
	}
	const maxChars = 16 * 1024
	size := func(t butlerthreads.Turn) int {
		n := len(t.Prompt) + len(t.Answer)
		for _, s := range t.Steps {
			n += len(s.Result) + len(s.Error)
		}
		return n
	}
	total := 0
	for _, t := range turns {
		total += size(t)
	}
	start := 0
	for total > maxChars && start < len(turns) {
		total -= size(turns[start])
		start++
	}
	turns = turns[start:]

	var out []map[string]any
	for _, t := range turns {
		if t.TurnID == "" {
			continue
		}
		// The just-reserved placeholder replays as agent.Run's
		// userPrompt, not history.
		if t.Answer == "" && t.Error == nil && t.Prompt == userPrompt {
			continue
		}
		if strings.TrimSpace(t.Prompt) == "" && strings.TrimSpace(t.Answer) == "" && len(t.Steps) == 0 {
			continue
		}
		if strings.TrimSpace(t.Prompt) != "" {
			out = append(out, map[string]any{"role": "user", "content": t.Prompt})
		}
		if t.Error != nil {
			continue
		}
		var toolSteps []any
		for _, s := range t.Steps {
			if strings.TrimSpace(s.Tool) == "" {
				continue
			}
			if strings.TrimSpace(s.Result) == "" && strings.TrimSpace(s.Error) == "" {
				continue
			}
			toolSteps = append(toolSteps, map[string]any{
				"tool": s.Tool, "args": s.Args,
				"output": s.Result, "error": s.Error,
			})
		}
		if len(toolSteps) > 0 {
			out = append(out, map[string]any{
				"role": "assistant", "content": t.Answer,
				"toolSteps": toolSteps,
			})
		} else if strings.TrimSpace(t.Answer) != "" {
			out = append(out, map[string]any{"role": "assistant", "content": t.Answer})
		}
	}
	return out
}
