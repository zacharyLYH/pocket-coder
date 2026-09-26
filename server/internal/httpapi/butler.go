// Butler endpoints: one global thread list (no project scope), one turn
// POST running a bounded loop with SSE status lines. Read tools land in
// ckpt 5; this slice runs the loop with zero tools so the turn, SSE, and
// transcript round-trip is testable end to end.
package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/butlerthreads"
	"pcoder/internal/obs"
)

// butlerGuide is the system prompt: answers + setup help, never code.
const butlerGuide = `You are Butler, a setup assistant for the Pocket Coder app. ` +
	`Answer questions about projects, sessions, previews, harnesses, git, and connections. ` +
	`You never read repo content and never write code. If asked for code, refuse and point at the Codemap tab. ` +
	`Keep answers short. No repo-modifying commands on your own initiative.`

// butlerBusy serializes one turn globally: a second POST while one is in
// flight gets 409. Taken before the thread is reserved, so a 409 never
// leaves a stray thread behind. No id tracking: list always reports
// runningThreadId null (remount-into-run lands with read tools, ckpt 5)
// and delete 409s on busy alone.
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
		var running any // remount-into-run lands with read tools (ckpt 5)
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
		answer, steps, runErr := runButlerTurn(r, cfg, st, threadID, body.Prompt, emit)
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
			"steps": turn.Steps, "time": turn.Time.Format(time.RFC3339),
		})
	}
}

// runButlerTurn runs the bounded loop (up to 6 tool steps; zero tools in
// this slice) and streams one SSE status line per model round. It returns
// the final answer plus the recorded steps.
func runButlerTurn(r *http.Request, cfg agent.Config, st *butlerthreads.Store, threadID, prompt string, emit func(tool, msg string)) (string, []butlerthreads.Step, error) {
	th, _ := st.Get(threadID)
	var history []map[string]any
	for _, t := range th.Turns {
		if t.TurnID != "" && !(t.Answer == "" && t.Error == nil && t.Prompt == prompt) {
			// (The skip-condition drops the just-reserved placeholder: it
			// replays as agent.Run's userPrompt, not history.)
			history = append(history, map[string]any{"role": "user", "content": t.Prompt})
			if t.Error == nil && len(t.Answer) > 0 {
				history = append(history, map[string]any{"role": "assistant", "content": t.Answer})
			}
		}
	}
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
	answer, err := agent.Run(r.Context(), cfg, butlerGuide, prompt, history, nil, "", nil, 6, onTrace, nil)
	if err != nil {
		return "", steps, err
	}
	if steps == nil {
		steps = []butlerthreads.Step{}
	}
	emit("done", "answered")
	return answer, steps, nil
}

func summarize(s string) string {
	return strings.TrimSpace(cut(s, 120))
}
