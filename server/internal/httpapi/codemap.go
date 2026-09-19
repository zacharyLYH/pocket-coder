// Codemap endpoints: ask-about-the-code over the agent loop, plus the
// read only file reader the snippet overlay consumes.
//
// Chats live in per-thread folders (see codemapthreads), not events.log.
// Tools are read only. No write path exists in this file.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/codemap"
	"pcoder/internal/codemapthreads"
	"pcoder/internal/obs"
)

// codemapBusy serializes one run per project: a second POST while one is
// in flight gets 409 codemap busy instead of burning a second loop.
// The value is the in-flight thread ID ("" only transiently before a new
// chat reserves its folder), so DELETE can refuse to drop a thread
// mid-run while still allowing unrelated threads through, and GET
// threads can surface it as runningThreadId for remount-into-run.
var codemapBusy = struct {
	mu sync.Mutex
	m  map[string]string
}{m: map[string]string{}}

func codemapTake(id, threadID string) bool {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	if _, ok := codemapBusy.m[id]; ok {
		return false
	}
	codemapBusy.m[id] = threadID
	return true
}

func codemapRunning(id string) (string, bool) {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	tid, ok := codemapBusy.m[id]
	return tid, ok
}

// codemapSet points the busy slot at a fresh folder id so same-thread
// DELETE 409s and GET threads reports it while the first turn runs.
func codemapSet(id, threadID string) {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	if _, ok := codemapBusy.m[id]; ok {
		codemapBusy.m[id] = threadID
	}
}

// writeCodemapErr answers a failed turn with the thread identity intact,
// so the client can open the failed placeholder turn and retry instead
// of losing it.
func writeCodemapErr(w http.ResponseWriter, status int, msg, threadID, threadTitle string) {
	body := map[string]any{"error": msg}
	if threadID != "" {
		body["threadId"] = threadID
	}
	if threadTitle != "" {
		body["threadTitle"] = threadTitle
	}
	writeJSON(w, status, body)
}

func codemapDone(id string) {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	delete(codemapBusy.m, id)
}

// repoSHA returns HEAD's sha, best effort: empty on failure (fresh repo,
// no HEAD yet). Never an error.
func repoSHA(d Deps, r *http.Request, container, dir string) string {
	out, err := d.Sessions.ExecCommand(r.Context(), container,
		"git -C "+shellQuote(dir)+" rev-parse HEAD 2>/dev/null || true")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func handleCodemap(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Reserve/store failures below already log specifically (codemap.turn
		// key); this deferred error covers only the pre-reserve failures
		// that have no specific line yet.
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapAsk, "codemap ask failed", err, nil)
			}
		}()
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			err = errors.New("ai not configured")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		var body struct {
			Prompt   string `json:"prompt"`
			ThreadID string `json:"threadId"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		prompt := strings.TrimSpace(body.Prompt)
		if prompt == "" {
			err = errors.New("prompt is required")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len([]rune(prompt)) > 4000 {
			err = errors.New("prompt over 4000 chars")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		threadID := strings.TrimSpace(body.ThreadID)
		if !codemapTake(id, threadID) {
			obs.Info(r.Context(), obs.CodemapBusy, "run rejected: previous run still in flight", map[string]any{"prompt": excerpt2000(prompt)})
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapDone(id)

		st := d.Codemaps
		if st == nil {
			err = errStoreUnconfigured
			writeInternalErr(w, "codemap store", err)
			return
		}
		// The placeholder exists before the model runs so a failed turn is
		// still retryable instead of lost.
		var history []map[string]any
		var turnN int
		var turnID string
		threadTitle := ""
		if threadID != "" {
			th, gerr := st.Get(id, threadID)
			if gerr != nil {
				err = errors.New("unknown thread")
				writeUnknownThread(w)
				return
			}
			threadTitle = th.Title
			// History from prior N.json turn files ONLY (lineage never
			// read), excluding the new placeholder reserved below.
			history = threadHistory(th)
			sha := repoSHA(d, r, container, dir)
			n, tid, rerr := st.ReserveFollowup(id, threadID, prompt, sha)
			if rerr != nil {
				if rerr.Error() == "unknown thread" {
					writeUnknownThread(w)
					return
				}
				obs.Error(r.Context(), obs.CodemapTurn, "turn reserve failed: "+rerr.Error(), map[string]any{"stage": "reserve_turn", "error": rerr.Error()})
				writeCodemapErr(w, http.StatusInternalServerError, "reserve turn: "+rerr.Error(), threadID, threadTitle)
				return
			}
			turnN, turnID = n, tid
			obs.Info(r.Context(), obs.CodemapTurnReserved, fmt.Sprintf("[%s] turn %d reserved before generation", tid, n), map[string]any{"turnId": tid, "threadId": threadID, "turn": n, "stage": "reserve_turn"})
		} else {
			sha := repoSHA(d, r, container, dir)
			tid, turn, rerr := st.ReserveNewThread(id, prompt, sha)
			if rerr != nil {
				obs.Error(r.Context(), obs.CodemapTurn, "thread initialization failed: "+rerr.Error(), map[string]any{"stage": "reserve_thread", "error": rerr.Error()})
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create thread: " + rerr.Error(), "stage": "reserve_thread"})
				return
			}
			threadID = tid
			turnID = turn
			turnN = 1
			threadTitle = codemapthreads.TitleFromPrompt(prompt)
			codemapSet(id, threadID)
			obs.Info(r.Context(), obs.CodemapThreadInitialized, fmt.Sprintf("[%s] thread + turn 1 reserved before generation; waiting for turn", threadID), map[string]any{"threadId": threadID, "title": threadTitle, "stage": "reserve_thread"})
		}

		executeReservedTurn(d, w, r, id, container, dir, cfg, st, threadID, threadTitle, turnN, turnID, prompt, history)
	}
}

// handleCodemapRetry reruns the LAST (highest-N) turn, which must be
// failed/crashed. The turn's N.json + N.lineage.json are rewritten from
// scratch (same turnId/path/prompt, fresh time+sha) and the full
// pipeline reruns with context turns 1..N-1 only. Response mirrors
// POST /codemap.
func handleCodemapRetry(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapRetry, "codemap retry failed", err, map[string]any{"threadId": tid})
			}
		}()
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			err = errors.New("ai not configured")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		if !codemapTake(id, tid) {
			obs.Info(r.Context(), obs.CodemapBusy, "retry rejected: previous run still in flight", map[string]any{"threadId": tid})
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapDone(id)

		st := d.Codemaps
		if st == nil {
			err = errStoreUnconfigured
			writeInternalErr(w, "codemap store", err)
			return
		}
		th, gerr := st.Get(id, tid)
		if gerr != nil {
			err = errors.New("unknown thread")
			writeUnknownThread(w)
			return
		}
		if len(th.Turns) == 0 {
			err = errors.New("unknown thread")
			writeUnknownThread(w)
			return
		}
		last := th.Turns[len(th.Turns)-1]
		if last.Error == nil && len(last.Sections) > 0 && string(last.Sections) != "null" {
			err = errors.New("retry only failed turns")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		// History is turns 1..N-1 only: the failed attempt never feeds
		// its own rerun.
		history := threadHistory(codemapthreads.Thread{
			ID: th.ID, Project: th.Project, Title: th.Title,
			CreatedAt: th.CreatedAt, UpdatedAt: th.UpdatedAt,
			Turns: th.Turns[:len(th.Turns)-1],
		})
		sha := repoSHA(d, r, container, dir)
		n, turnID, prompt, rerr := st.BeginRetry(id, tid, sha)
		if rerr != nil {
			if rerr.Error() == "unknown thread" {
				writeUnknownThread(w)
				return
			}
			obs.Error(r.Context(), obs.CodemapTurn, "retry reserve failed: "+rerr.Error(), map[string]any{"stage": "retry_reserve", "error": rerr.Error()})
			writeCodemapErr(w, http.StatusInternalServerError, "retry reserve: "+rerr.Error(), tid, th.Title)
			return
		}
		obs.Info(r.Context(), obs.CodemapRetryReserved, fmt.Sprintf("[%s] turn %d rewritten for retry", turnID, n), map[string]any{"turnId": turnID, "threadId": tid, "turn": n, "stage": "retry_reserve"})
		executeReservedTurn(d, w, r, id, container, dir, cfg, st, tid, th.Title, n, turnID, prompt, history)
	}
}

// executeReservedTurn runs the model for an already-reserved placeholder
// turn and persists via Complete/Fail. Manifest untouched after create.
// Post-manifest errors keep threadId+title so FE can open the failed
// placeholder. Callers hold the codemapBusy slot.
func executeReservedTurn(d Deps, w http.ResponseWriter, r *http.Request, id, container, dir string, cfg agent.Config, st *codemapthreads.Store, threadID, threadTitle string, turnN int, turnID, prompt string, history []map[string]any) {
	r = r.WithContext(obs.WithProject(r.Context(), id))
	sha := repoSHA(d, r, container, dir)
	runStart := time.Now()
	toolStarts := map[string]time.Time{}
	obs.Info(r.Context(), obs.CodemapStart, fmt.Sprintf("ask %s | model=%s sha=%s history=%d chars=%d: %s", turnID, cfg.Model, sha, len(history), len(prompt), excerpt2000(prompt)),
		map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "sha": sha, "history": len(history), "prompt": capData(prompt, 500)})
	// Codemap turns are batch jobs over slow reasoning tiers: many
	// tool rounds plus retries can run several minutes.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	step := 0
	res, rounds, lineage, err := codemap.Ask(ctx, cfg, d.Sessions, container, dir, prompt, history,
		func(ev agent.TraceEvent) {
			switch ev.Kind {
			case "round":
				obs.Info(r.Context(), obs.CodemapRound, fmt.Sprintf("[%s round %d] %s", turnID, ev.Round+1, excerpt2000(ev.Text)),
					map[string]any{"turnId": turnID, "round": ev.Round + 1})
			case "tool_start":
				step++
				toolStarts[ev.Tool+"\x00"+ev.Args] = time.Now()
				obs.Info(r.Context(), obs.CodemapTool, fmt.Sprintf("[%s step %d] %s %s", turnID, step, ev.Tool, excerpt2000(ev.Args)),
					map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool})
			case "tool_done":
				started := toolStarts[ev.Tool+"\x00"+ev.Args]
				ms := int64(0)
				if !started.IsZero() {
					ms = time.Since(started).Milliseconds()
				}
				if ev.Err != "" {
					obs.Error(r.Context(), obs.CodemapTool, fmt.Sprintf("[%s] %s failed in %dms: %s", turnID, ev.Tool, ms, excerpt2000(ev.Err)),
						map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "error": capData(ev.Err, 4000), "durationMs": ms})
				} else {
					obs.Info(r.Context(), obs.CodemapToolResult, fmt.Sprintf("[%s] %s done in %dms (%d chars): %s", turnID, ev.Tool, ms, len(ev.Result), excerpt2000(ev.Result)),
						map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "chars": len(ev.Result), "durationMs": ms})
				}
			case "model_error":
				obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] model=%s failed after %dms: %s", turnID, cfg.Model, time.Since(runStart).Milliseconds(), excerpt2000(ev.Err)),
					map[string]any{"turnId": turnID, "model": cfg.Model, "error": capData(ev.Err, 4000)})
			case "ref_drop":
				obs.Info(r.Context(), obs.CodemapRefDropped, fmt.Sprintf("[%s] ref dropped %s %s: %s", turnID, ev.Tool, excerpt2000(ev.Args), excerpt2000(ev.Result)),
					map[string]any{"turnId": turnID, "path": ev.Tool, "range": ev.Args, "reason": ev.Result})
			}
		})
	// Keep the complete debug graph separate from the FE turn record.
	// It is written on both success and failure so failed provider calls
	// remain diagnosable.
	var lineageRaw []byte
	if lineage != nil {
		lineage.TurnID = turnID
		lineage.ThreadID = threadID
		lineage.Prompt = prompt
		lineage.Time = time.Now().UTC()
		if err != nil {
			lineage.Error = err.Error()
		}
		if raw, merr := json.Marshal(lineage); merr == nil {
			lineageRaw = raw
		} else {
			obs.Error(r.Context(), obs.CodemapLineage, fmt.Sprintf("[%s] lineage marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "error": merr.Error(), "stage": "marshal_lineage"})
		}
	}
	if err != nil {
		obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] failed after %dms: %s", turnID, time.Since(runStart).Milliseconds(), excerpt2000(err.Error())),
			map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "error": capData(err.Error(), 4000), "durationMs": time.Since(runStart).Milliseconds()})
		// The placeholder stays visible with its error so the turn is
		// retryable; the failure graph lands beside it in lineage.
		now := time.Now().UTC()
		if ferr := st.FailTurn(id, threadID, turnN, codemapthreads.Turn{
			TurnID: turnID, SHA: sha, Prompt: prompt, Time: now,
		}, err.Error(), lineageRaw); ferr != nil {
			obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] fail persist failed: %s", turnID, ferr), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "fail_turn", "error": ferr.Error()})
		} else {
			obs.Info(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] failed turn persisted with error", turnID), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "fail_turn"})
		}
		if ctx.Err() != nil {
			writeCodemapErr(w, http.StatusGatewayTimeout, "codemap timed out", threadID, threadTitle)
		} else {
			writeCodemapErr(w, http.StatusBadGateway, err.Error(), threadID, threadTitle)
		}
		return
	}
	steps := flattenRounds(rounds)
	// events.log keeps only an audit line (never sections); raw tier-2
	// text lives only in lineage format_response events, never in N.json.
	sectionsRaw, merr := json.Marshal(res.Sections)
	if merr != nil {
		obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] sections marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_sections", "error": merr.Error()})
		writeCodemapErr(w, http.StatusInternalServerError, "marshal codemap sections: "+merr.Error(), threadID, threadTitle)
		return
	}
	toolsRaw, merr := json.Marshal(rounds)
	if merr != nil {
		obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] tools marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_tools", "error": merr.Error()})
		writeCodemapErr(w, http.StatusInternalServerError, "marshal codemap tools: "+merr.Error(), threadID, threadTitle)
		return
	}
	now := time.Now().UTC()
	if cerr := st.CompleteTurn(id, threadID, turnN, codemapthreads.Turn{
		TurnID: turnID, SHA: sha, Prompt: prompt,
		Sections: json.RawMessage(sectionsRaw), Tools: json.RawMessage(toolsRaw),
		Time: now,
	}, lineageRaw); cerr != nil {
		obs.Error(r.Context(), obs.CodemapTurn, fmt.Sprintf("[%s] turn complete failed: %s", turnID, cerr), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "complete_turn", "error": cerr.Error()})
		if cerr.Error() == "unknown thread" {
			writeCodemapErr(w, http.StatusNotFound, "unknown thread", threadID, threadTitle)
			return
		}
		writeCodemapErr(w, http.StatusInternalServerError, "complete turn: "+cerr.Error(), threadID, threadTitle)
		return
	}
	obs.Info(r.Context(), obs.CodemapTurnSaved, fmt.Sprintf("[%s] thread turn persisted: title=%q sections=%d rounds=%d", turnID, threadTitle, len(res.Sections), len(rounds)), map[string]any{"turnId": turnID, "threadId": threadID, "title": threadTitle, "sections": len(res.Sections), "rounds": len(rounds), "stage": "complete_turn"})
	if lineageRaw != nil {
		obs.Info(r.Context(), obs.CodemapLineage, fmt.Sprintf("[%s] lineage saved (%d bytes)", turnID, len(lineageRaw)), map[string]any{"turnId": turnID, "bytes": len(lineageRaw), "stage": "save_lineage"})
	}
	obs.Info(r.Context(), obs.CodemapDone, fmt.Sprintf("[%s] done in %dms: %d section(s), %d tool call(s)",
		turnID, time.Since(runStart).Milliseconds(), len(res.Sections), len(steps)),
		map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "sha": sha,
			"sections": len(res.Sections), "tools": len(steps), "durationMs": time.Since(runStart).Milliseconds()})
	// The audit closes the turn: it stays the last event so readers can
	// treat "last event is codemap.turn" as run completion.
	_, _ = d.Events.Append("codemap.turn", map[string]any{
		"project": id, "threadId": threadID, "turnId": turnID,
		"prompt": capData(prompt, 500), "sections": len(res.Sections), "tools": len(steps),
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"turnId": turnID, "sha": sha, "sections": res.Sections, "tools": steps,
		"threadId": threadID, "threadTitle": threadTitle,
		"time": now.Format(time.RFC3339),
	})
}

// cut bounds s at max runes (rune-aware: byte slicing could split a
// multi-byte rune). excerpt2000 trims and names empties for log lines;
// capData is the same cut for log entry data.
func cut(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}

func excerpt2000(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	return cut(s, 2000)
}

func capData(s string, max int) string { return cut(s, max) }

// flattenRounds collapses persisted rounds into one step list for the
// Steps panel response (order preserved). Never nil, so the response
// carries [] instead of null when no tools ran.
func flattenRounds(rounds []codemap.ToolRound) []codemap.ToolStep {
	out := []codemap.ToolStep{}
	for _, r := range rounds {
		out = append(out, r.Steps...)
	}
	return out
}

// parseRounds decodes Turn.Tools ([]ToolRound, laid out as the turn made
// them). Unknown bytes replay as nothing rather than failing the turn.
func parseRounds(raw json.RawMessage) []codemap.ToolRound {
	if len(raw) == 0 {
		return nil
	}
	var rounds []codemap.ToolRound
	if err := json.Unmarshal(raw, &rounds); err != nil {
		return nil
	}
	return rounds
}

// sectionsTranscript renders a prior answer as a natural-language
// transcript instead of raw JSON, so follow-ups read conversation, not
// schema: numbered sections with one-line ref handoffs.
func sectionsTranscript(sections []codemap.Section) string {
	var sb strings.Builder
	for i, s := range sections {
		if i > 0 {
			sb.WriteString("\n")
		}
		title := strings.TrimSpace(s.Title)
		summary := strings.TrimSpace(s.Summary)
		if title != "" && summary != "" {
			fmt.Fprintf(&sb, "%d. %s — %s", i+1, title, summary)
		} else if title != "" {
			fmt.Fprintf(&sb, "%d. %s", i+1, title)
		} else if summary != "" {
			fmt.Fprintf(&sb, "%d. %s", i+1, summary)
		} else {
			fmt.Fprintf(&sb, "%d. (empty section)", i+1)
		}
		for _, r := range s.Refs {
			snip := strings.TrimSpace(r.Snippet)
			if rs := []rune(snip); len(rs) > 200 {
				snip = string(rs[:200]) + "…"
			}
			snip = strings.ReplaceAll(snip, "\n", " / ")
			loc := fmt.Sprintf("%s:%d-%d", r.Path, r.StartLine, r.EndLine)
			if fn := strings.TrimSpace(r.Function); fn != "" {
				loc += " " + fn + "()"
			}
			if snip != "" {
				fmt.Fprintf(&sb, "\n   - %s — %s", loc, snip)
			} else {
				fmt.Fprintf(&sb, "\n   - %s", loc)
			}
		}
	}
	return sb.String()
}

// threadHistory rebuilds the LLM conversation from persisted turns:
// each turn becomes user(prompt) [+ one assistant(toolSteps) per tool
// round, laid out as the turn made them + assistant(transcript)]. Lineage
// files are NEVER read for context: history comes exclusively from N.json
// turn files (prompt + sections + tools per turn). Failed/crashed turns
// (error set or answer-less) contribute their prompt only: no sections
// to replay, no tool block without outputs. Context is bounded to the
// last 20 turns and ~16KB estimated chars.
func threadHistory(th codemapthreads.Thread) []map[string]any {
	turns := th.Turns
	if len(turns) > 20 {
		turns = turns[len(turns)-20:]
	}
	// Byte-budget from the tail: drop oldest until estimated chars fit.
	const maxChars = 16 * 1024
	size := func(t codemapthreads.Turn) int {
		return len(t.Prompt) + len(t.Sections) + len(t.Tools)
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
		if strings.TrimSpace(t.Prompt) == "" && len(t.Sections) == 0 && len(t.Tools) == 0 {
			continue
		}
		if strings.TrimSpace(t.Prompt) != "" {
			out = append(out, map[string]any{"role": "user", "content": t.Prompt})
		}
		// Answer-less placeholders (in-flight reserve, crash) and failed
		// turns replay prompt-only: nothing grounded to replay.
		if t.Error != nil {
			continue
		}
		if len(t.Tools) > 0 {
			for _, r := range parseRounds(t.Tools) {
				hasOutput := false
				for _, s := range r.Steps {
					if strings.TrimSpace(s.Output) != "" || strings.TrimSpace(s.Err) != "" {
						hasOutput = true
						break
					}
				}
				if !hasOutput {
					continue
				}
				toolSteps := make([]any, 0, len(r.Steps))
				for _, s := range r.Steps {
					toolSteps = append(toolSteps, map[string]any{
						"tool": s.Tool, "args": s.Args,
						"output": s.Output, "error": s.Err,
					})
				}
				thought := strings.TrimSpace(r.Thought)
				if thought == "" {
					thought = "(calling tools)"
				}
				out = append(out, map[string]any{
					"role": "assistant", "content": thought,
					"toolSteps": toolSteps,
				})
			}
		}
		if raw := strings.TrimSpace(string(t.Sections)); raw != "" && raw != "null" {
			var sections []codemap.Section
			if err := json.Unmarshal(t.Sections, &sections); err == nil && len(sections) > 0 {
				out = append(out, map[string]any{"role": "assistant", "content": sectionsTranscript(sections)})
			} else {
				out = append(out, map[string]any{"role": "assistant", "content": raw})
			}
		}
	}
	return out
}

func handleCodemapFile(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapFile, "read file failed", err, map[string]any{"path": r.URL.Query().Get("path")})
			}
		}()
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			err = errors.New("repo unavailable")
			return
		}
		q := r.URL.Query()
		path := q.Get("path")
		if !validGitPath(path) {
			err = errors.New("invalid path")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		start, serr := strconv.Atoi(q.Get("start"))
		end, eerr := strconv.Atoi(q.Get("end"))
		if serr != nil || eerr != nil || start < 1 || end < start || end-start > 119 {
			err = errors.New("start/end must span 1-120 lines with start >= 1")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		out, xerr := d.Sessions.ExecCommand(r.Context(), container,
			fmt.Sprintf("sed -n '%d,%dp' %s", start, end, shellQuote(dir+"/"+path)))
		if xerr != nil {
			// Missing file (deleted since generation) reads as empty, not 500.
			if strings.Contains(xerr.Error(), "exit ") && strings.TrimSpace(out) == "" {
				writeJSON(w, http.StatusOK, map[string]any{
					"path": path, "content": "", "binary": false, "moved": true,
					"sha": repoSHA(d, r, container, dir),
				})
				return
			}
			err = xerr
			writeInternalErr(w, "read file", xerr)
			return
		}
		if strings.IndexByte(out, 0) >= 0 {
			writeJSON(w, http.StatusOK, map[string]any{
				"path": path, "content": "", "binary": true, "moved": false,
				"sha": repoSHA(d, r, container, dir),
			})
			return
		}
		if len(out) > 100*1024 {
			// Cap by runes to avoid splitting a multi-byte rune at the cut.
			if r := []rune(out); len(r) > 100*1024 {
				out = string(r[:100*1024])
			}
		}
		sha := repoSHA(d, r, container, dir)
		moved := false
		if want := q.Get("sha"); want != "" && sha != "" && want != sha {
			moved = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "content": out, "binary": false, "moved": moved, "sha": sha,
		})
	}
}
