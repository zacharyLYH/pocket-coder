// Codemap endpoints: ask-about-the-code over the agent loop, plus the
// read only file reader the snippet overlay consumes. Chats live in
// per-thread folders (see codemapthreads). Tools are read only.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/codemap"
	"pcoder/internal/obs"
	"pcoder/internal/threads"
)

// writeTurnErr answers a failed turn with the thread identity intact,
// so the client can open the failed placeholder turn and retry instead
// of losing it. Shared by codemap and butler turns.
func writeTurnErr(w http.ResponseWriter, status int, msg, threadID, threadTitle string) {
	writeJSON(w, status, turnErrBody(msg, threadID, threadTitle))
}

// repoSHA returns HEAD's sha, best effort: empty on failure (fresh repo,
// no HEAD yet). Never an error.
func repoSHA(ctx context.Context, d Deps, container, dir string) string {
	out, err := d.Sessions.ExecCommand(ctx, container,
		"git -C "+shellQuote(dir)+" rev-parse HEAD 2>/dev/null || true")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func handleCodemap(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Deferred error covers only the pre-reserve failures: reserve and
		// store failures below log specifically (codemap.turn key).
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
		if !codemapRuns.take(id, threadID) {
			obs.Info(r.Context(), obs.CodemapBusy, "run rejected: previous run still in flight", map[string]any{"prompt": excerpt2000(prompt)})
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapRuns.done(id)

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
			sha := repoSHA(r.Context(), d, container, dir)
			n, tid, rerr := st.ReserveFollowup(id, threadID, prompt, sha, nil)
			if rerr != nil {
				if rerr.Error() == "unknown thread" {
					writeUnknownThread(w)
					return
				}
				obs.Error(r.Context(), obs.CodemapTurn, "turn reserve failed: "+rerr.Error(), map[string]any{"stage": "reserve_turn", "error": rerr.Error()})
				writeTurnErr(w, http.StatusInternalServerError, "reserve turn: "+rerr.Error(), threadID, threadTitle)
				return
			}
			turnN, turnID = n, tid
			obs.Info(r.Context(), obs.CodemapTurnReserved, fmt.Sprintf("[%s] turn %d reserved before generation", tid, n), map[string]any{"turnId": tid, "threadId": threadID, "turn": n, "stage": "reserve_turn"})
		} else {
			sha := repoSHA(r.Context(), d, container, dir)
			tid, turn, rerr := st.ReserveNewThread(id, prompt, sha, nil)
			if rerr != nil {
				obs.Error(r.Context(), obs.CodemapTurn, "thread initialization failed: "+rerr.Error(), map[string]any{"stage": "reserve_thread", "error": rerr.Error()})
				writeTurnErr(w, http.StatusInternalServerError, "create thread: "+rerr.Error(), "", "")
				return
			}
			threadID = tid
			turnID = turn
			turnN = 1
			threadTitle = threads.TitleFromPrompt(prompt)
			codemapRuns.set(id, threadID)
			obs.Info(r.Context(), obs.CodemapThreadInitialized, fmt.Sprintf("[%s] thread + turn 1 reserved before generation; waiting for turn", threadID), map[string]any{"threadId": threadID, "title": threadTitle, "stage": "reserve_thread"})
		}

		tstatus, tbody := runCodemapTurn(detached(r).Context(), d, st, id, container, dir, cfg, threadID, threadTitle, turnN, turnID, prompt, history)
		writeJSON(w, tstatus, tbody)
	}
}

// handleCodemapRetry reruns the LAST (highest-N) turn, which must be
// failed/crashed. The turn is rewritten from scratch and the full
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
		if !codemapRuns.take(id, tid) {
			obs.Info(r.Context(), obs.CodemapBusy, "retry rejected: previous run still in flight", map[string]any{"threadId": tid})
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapRuns.done(id)

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
		lastPayload := codemapPayloadOf(last)
		if last.Error == nil && len(lastPayload.Sections) > 0 && string(lastPayload.Sections) != "null" {
			err = errors.New("retry only failed turns")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		// History is turns 1..N-1 only: the failed attempt never feeds
		// its own rerun.
		history := threadHistory(threads.Thread{
			ID: th.ID, Title: th.Title,
			CreatedAt: th.CreatedAt, UpdatedAt: th.UpdatedAt,
			Turns: th.Turns[:len(th.Turns)-1],
		})
		sha := repoSHA(r.Context(), d, container, dir)
		n, turnID, prompt, rerr := st.BeginRetry(id, tid, sha)
		if rerr != nil {
			if rerr.Error() == "unknown thread" {
				writeUnknownThread(w)
				return
			}
			obs.Error(r.Context(), obs.CodemapTurn, "retry reserve failed: "+rerr.Error(), map[string]any{"stage": "retry_reserve", "error": rerr.Error()})
			writeTurnErr(w, http.StatusInternalServerError, "retry reserve: "+rerr.Error(), tid, th.Title)
			return
		}
		obs.Info(r.Context(), obs.CodemapRetryReserved, fmt.Sprintf("[%s] turn %d rewritten for retry", turnID, n), map[string]any{"turnId": turnID, "threadId": tid, "turn": n, "stage": "retry_reserve"})
		tstatus, tbody := runCodemapTurn(detached(r).Context(), d, st, id, container, dir, cfg, tid, th.Title, n, turnID, prompt, history)
		writeJSON(w, tstatus, tbody)
	}
}

// runCodemapTurn runs the model for an already-reserved placeholder
// turn and persists via Complete/Fail. Post-manifest errors keep
// threadId+title so FE can open the failed placeholder. Callers hold the
// codemapBusy slot.
// runCodemapTurn runs the model for an already-reserved placeholder turn
// and persists via Complete/Fail. It returns the HTTP status + body the
// caller answers with, so the fire-and-forget explain path needs no fake
// ResponseWriter. Callers hold the slot; ctx is detached from the request.
func runCodemapTurn(ctx context.Context, d Deps, st *threads.Store, id, container, dir string, cfg agent.Config, threadID, threadTitle string, turnN int, turnID, prompt string, history []map[string]any) (int, map[string]any) {
	sha := repoSHA(ctx, d, container, dir)
	runStart := time.Now()
	toolStarts := map[string]time.Time{}
	obs.Info(ctx, obs.CodemapStart, fmt.Sprintf("ask %s | model=%s sha=%s history=%d chars=%d: %s", turnID, cfg.Model, sha, len(history), len(prompt), excerpt2000(prompt)),
		map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "sha": sha, "history": len(history), "prompt": capData(prompt, 500)})
	// Codemap turns are batch jobs over slow reasoning tiers: many
	// tool rounds plus retries can run several minutes.
	tctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	step := 0
	res, steps, lineage, err := codemap.Ask(tctx, cfg, d.Sessions, container, dir, prompt, history,
		func(ev agent.TraceEvent) {
			switch ev.Kind {
			case "round":
				obs.Info(ctx, obs.CodemapRound, fmt.Sprintf("[%s round %d] %s", turnID, ev.Round+1, excerpt2000(ev.Text)),
					map[string]any{"turnId": turnID, "round": ev.Round + 1})
			case "tool_start":
				step++
				toolStarts[ev.Tool+"\x00"+ev.Args] = time.Now()
				obs.Info(ctx, obs.CodemapTool, fmt.Sprintf("[%s step %d] %s %s", turnID, step, ev.Tool, excerpt2000(ev.Args)),
					map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool})
			case "tool_done":
				started := toolStarts[ev.Tool+"\x00"+ev.Args]
				ms := int64(0)
				if !started.IsZero() {
					ms = time.Since(started).Milliseconds()
				}
				if ev.Err != "" {
					obs.Error(ctx, obs.CodemapTool, fmt.Sprintf("[%s] %s failed in %dms: %s", turnID, ev.Tool, ms, excerpt2000(ev.Err)),
						map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "error": capData(ev.Err, 4000), "durationMs": ms})
				} else {
					obs.Info(ctx, obs.CodemapToolResult, fmt.Sprintf("[%s] %s done in %dms (%d chars): %s", turnID, ev.Tool, ms, len(ev.Result), excerpt2000(ev.Result)),
						map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "chars": len(ev.Result), "durationMs": ms})
				}
			case "model_error":
				obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] model=%s failed after %dms: %s", turnID, cfg.Model, time.Since(runStart).Milliseconds(), excerpt2000(ev.Err)),
					map[string]any{"turnId": turnID, "model": cfg.Model, "error": capData(ev.Err, 4000)})
			case "ref_drop":
				obs.Info(ctx, obs.CodemapRefDropped, fmt.Sprintf("[%s] ref dropped %s %s: %s", turnID, ev.Tool, excerpt2000(ev.Args), excerpt2000(ev.Result)),
					map[string]any{"turnId": turnID, "path": ev.Tool, "range": ev.Args, "reason": ev.Result})
			}
		})
	// Lineage is the complete debug graph, separate from the FE turn
	// record; written on both success and failure.
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
			obs.Error(ctx, obs.CodemapLineage, fmt.Sprintf("[%s] lineage marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "error": merr.Error(), "stage": "marshal_lineage"})
		}
	}
	if err != nil {
		obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] failed after %dms: %s", turnID, time.Since(runStart).Milliseconds(), excerpt2000(err.Error())),
			map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "error": capData(err.Error(), 4000), "durationMs": time.Since(runStart).Milliseconds()})
		// The placeholder stays visible with its error so the turn is
		// retryable; the failure graph lands beside it in lineage.
		now := time.Now().UTC()
		msg := err.Error()
		if ferr := st.CompleteTurn(id, threadID, turnN, threads.Turn{
			TurnID: turnID, SHA: sha, Prompt: prompt, Time: now, Error: &msg,
		}, lineageRaw); ferr != nil {
			obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] fail persist failed: %s", turnID, ferr), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "fail_turn", "error": ferr.Error()})
		} else {
			obs.Info(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] failed turn persisted with error", turnID), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "fail_turn"})
		}
		if tctx.Err() != nil {
			return http.StatusGatewayTimeout, turnErrBody("codemap timed out", threadID, threadTitle)
		}
		return http.StatusBadGateway, turnErrBody(err.Error(), threadID, threadTitle)
	}
	// Steps are already flat (butler shape): one list for the response,
	// the audit counts, and persistence. Never nil, so the response
	// carries [] instead of null when no tools ran.
	if steps == nil {
		steps = []agent.Step{}
	}
	// events.log keeps only an audit line (never sections).
	sectionsRaw, merr := json.Marshal(res.Sections)
	if merr != nil {
		obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] sections marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_sections", "error": merr.Error()})
		return http.StatusInternalServerError, turnErrBody("marshal codemap sections: "+merr.Error(), threadID, threadTitle)
	}
	toolsRaw, merr := json.Marshal(steps)
	if merr != nil {
		obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] tools marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_tools", "error": merr.Error()})
		return http.StatusInternalServerError, turnErrBody("marshal codemap tools: "+merr.Error(), threadID, threadTitle)
	}
	now := time.Now().UTC()
	if cerr := st.CompleteTurn(id, threadID, turnN, threads.Turn{
		TurnID: turnID, SHA: sha, Prompt: prompt,
		Payload: codemapPayload(sectionsRaw, toolsRaw),
		Time:    now,
	}, lineageRaw); cerr != nil {
		obs.Error(ctx, obs.CodemapTurn, fmt.Sprintf("[%s] turn complete failed: %s", turnID, cerr), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "complete_turn", "error": cerr.Error()})
		if cerr.Error() == "unknown thread" {
			return http.StatusNotFound, turnErrBody("unknown thread", threadID, threadTitle)
		}
		return http.StatusInternalServerError, turnErrBody("complete turn: "+cerr.Error(), threadID, threadTitle)
	}
	obs.Info(ctx, obs.CodemapTurnSaved, fmt.Sprintf("[%s] thread turn persisted: title=%q sections=%d steps=%d", turnID, threadTitle, len(res.Sections), len(steps)), map[string]any{"turnId": turnID, "threadId": threadID, "title": threadTitle, "sections": len(res.Sections), "steps": len(steps), "stage": "complete_turn"})
	if lineageRaw != nil {
		obs.Info(ctx, obs.CodemapLineage, fmt.Sprintf("[%s] lineage saved (%d bytes)", turnID, len(lineageRaw)), map[string]any{"turnId": turnID, "bytes": len(lineageRaw), "stage": "save_lineage"})
	}
	obs.Info(ctx, obs.CodemapDone, fmt.Sprintf("[%s] done in %dms: %d section(s), %d tool call(s)",
		turnID, time.Since(runStart).Milliseconds(), len(res.Sections), len(steps)),
		map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "sha": sha,
			"sections": len(res.Sections), "steps": len(steps), "durationMs": time.Since(runStart).Milliseconds()})
	// The audit closes the turn: it stays the last event so readers can
	// treat "last event is codemap.turn" as run completion.
	_, _ = d.Events.Append("codemap.turn", map[string]any{
		"project": id, "threadId": threadID, "turnId": turnID,
		"prompt": capData(prompt, 500), "sections": len(res.Sections), "steps": len(steps),
	})
	return http.StatusOK, map[string]any{
		"turnId": turnID, "sha": sha, "sections": res.Sections, "steps": steps,
		"threadId": threadID, "threadTitle": threadTitle,
		"time": now.Format(time.RFC3339),
	}
}

// cut bounds s at max runes (rune-aware: byte slicing could split a
// multi-byte rune).
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

// parseSteps decodes Turn.Tools (flat []agent.Step, shared with butler). Threads
// persisted before the flattening carry []ToolRound instead — read those
// too, so old chats keep their tool context without a backfill.
func parseSteps(raw json.RawMessage) []agent.Step {
	if len(raw) == 0 {
		return nil
	}
	var steps []agent.Step
	if err := json.Unmarshal(raw, &steps); err == nil && allNamed(steps) {
		return steps
	}
	var rounds []codemap.ToolRound
	if err := json.Unmarshal(raw, &rounds); err != nil {
		return nil
	}
	out := []agent.Step{}
	for _, r := range rounds {
		out = append(out, r.Steps...)
	}
	return out
}

// allNamed guards the legacy fallback: encoding/json ignores unknown
// fields, so a []ToolRound payload "decodes" into tool-less steps unless
// we check. Empty input counts as named (nothing to dispute).
func allNamed(steps []agent.Step) bool {
	for _, s := range steps {
		if strings.TrimSpace(s.Tool) == "" {
			return false
		}
	}
	return true
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

// threadHistory rebuilds the LLM conversation from persisted turns (N.json
// files only — lineage files are NEVER read for context). Failed/crashed
// turns contribute their prompt only. Context is bounded to the last 20
// turns and ~16KB estimated chars.
// codemapPayload is codemap's opaque turn payload (stored in the shared
// threads envelope): sections + flat tool steps, marshaled once at complete.
func codemapPayload(sections, steps json.RawMessage) json.RawMessage {
	raw, err := json.Marshal(map[string]json.RawMessage{"sections": sections, "steps": steps})
	if err != nil {
		return nil
	}
	return raw
}

// codemapPayloadOf decodes one envelope turn's codemap payload: sections
// plus flat executed steps. Turns persisted before the rename carry the
// steps under "tools" — read those too, so old chats keep working.
func codemapPayloadOf(t threads.Turn) (p struct {
	Sections json.RawMessage `json:"sections"`
	Steps    json.RawMessage `json:"steps"`
}) {
	_ = json.Unmarshal(t.Payload, &p)
	if len(p.Steps) == 0 {
		var legacy struct {
			Steps json.RawMessage `json:"tools"`
		}
		if json.Unmarshal(t.Payload, &legacy) == nil {
			p.Steps = legacy.Steps
		}
	}
	return p
}

func threadHistory(th threads.Thread) []map[string]any {
	return agent.BuildHistory(codemapViews(th), "", agent.HistoryOpts{MaxTurns: 20, MaxChars: 16 * 1024})
}

// codemapViews maps persisted turns onto the shared history view: one
// assistant entry per turn carrying the sections transcript plus flat
// tool steps — the same shape BuildHistory replays for butler, so the
// bespoke sizing/assembly loop is gone.
func codemapViews(th threads.Thread) []agent.TurnView {
	out := make([]agent.TurnView, 0, len(th.Turns))
	for _, t := range th.Turns {
		p := codemapPayloadOf(t)
		v := agent.TurnView{Prompt: t.Prompt, Failed: t.Error != nil}
		if raw := strings.TrimSpace(string(p.Sections)); raw != "" && raw != "null" {
			var sections []codemap.Section
			if err := json.Unmarshal(p.Sections, &sections); err == nil && len(sections) > 0 {
				v.Answer = sectionsTranscript(sections)
			} else {
				v.Answer = raw
			}
		}
		for _, s := range parseSteps(p.Steps) {
			v.Steps = append(v.Steps, agent.Step{Tool: s.Tool, Args: s.Args, Output: s.Output, Err: s.Err})
		}
		out = append(out, v)
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
					"sha": repoSHA(r.Context(), d, container, dir),
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
				"sha": repoSHA(r.Context(), d, container, dir),
			})
			return
		}
		if len(out) > 100*1024 {
			// Cap by runes to avoid splitting a multi-byte rune at the cut.
			if r := []rune(out); len(r) > 100*1024 {
				out = string(r[:100*1024])
			}
		}
		sha := repoSHA(r.Context(), d, container, dir)
		moved := false
		if want := q.Get("sha"); want != "" && sha != "" && want != sha {
			moved = true
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "content": out, "binary": false, "moved": moved, "sha": sha,
		})
	}
}
