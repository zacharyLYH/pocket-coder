// Codemap endpoints: ask-about-the-code over the agent loop, plus the
// read only file reader the snippet overlay consumes.
//
// Chats live in per-thread files (see codemapthreads), not events.log.
// Tools are read only. No write path exists in this file.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/codemap"
	"pcoder/internal/codemapthreads"
)

// codemapBusy serializes one run per project: a second POST while one is
// in flight gets 409 codemap busy instead of burning a second loop.
// The value is the in-flight thread ID ("" for a new chat whose thread
// does not exist yet), so DELETE can refuse to drop a thread mid-run
// while still allowing unrelated threads through.
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

// codemapSet updates the in-flight thread for a project that already
// holds the slot (e.g. a new chat whose thread is created upfront).
func codemapSet(id, threadID string) {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	if _, ok := codemapBusy.m[id]; ok {
		codemapBusy.m[id] = threadID
	}
}

// writeCodemapErr answers a failed turn with the thread identity intact,
// so the client can open the (possibly empty) thread file and retry
// instead of losing it.
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
		cfg := aiConfig(d, aiBody{})
		if !cfg.Valid() {
			writeErr(w, http.StatusConflict, "ai not configured")
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
			writeErr(w, http.StatusBadRequest, "prompt is required")
			return
		}
		if len(prompt) > 4000 {
			writeErr(w, http.StatusBadRequest, "prompt over 4000 chars")
			return
		}
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		threadID := strings.TrimSpace(body.ThreadID)
		if !codemapTake(id, threadID) {
			plog(d, id, "codemap.busy", "run rejected: previous run still in flight", map[string]any{"prompt": excerpt2000(prompt)})
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapDone(id)

		st := d.Codemaps
		if st == nil {
			writeInternalErr(w, "codemap store", errStoreUnconfigured)
			return
		}
		// Server-authoritative context: the thread file is both the
		// record and the context source. Client-supplied history is
		// gone; rebuild from the persisted thread before the loop.
		// The thread file exists before the model runs: new chats are
		// created upfront so the log is on disk immediately, even if
		// generation fails.
		var history []map[string]any
		threadTitle := ""
		if threadID != "" {
			th, gerr := st.Get(id, threadID)
			if gerr != nil {
				writeUnknownThread(w)
				return
			}
			threadTitle = th.Title
			history = threadHistory(th)
		} else {
			th, cerr := st.Create(id, "")
			if cerr != nil {
				plog(d, id, "codemap.persistence_error", "thread initialization failed: "+cerr.Error(), map[string]any{"stage": "create_thread", "error": cerr.Error()})
				writeInternalErr(w, "create thread", cerr)
				return
			}
			threadID = th.ID
			threadTitle = th.Title
			codemapSet(id, threadID)
			plog(d, id, "codemap.thread_initialized", fmt.Sprintf("[%s] empty thread initialized before generation; waiting for turn append", threadID), map[string]any{"threadId": threadID, "title": threadTitle, "stage": "create_thread"})
		}

		sha := repoSHA(d, r, container, dir)
		turnID := codemapthreads.MintID()
		runStart := time.Now()
		toolStarts := map[string]time.Time{}
		plog(d, id, "codemap.start", fmt.Sprintf("ask %s | model=%s sha=%s history=%d chars=%d: %s", turnID, cfg.Model, sha, len(history), len(prompt), excerpt2000(prompt)),
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
					// Message already carries the excerpted thought; the
					// ring does not need a second copy of the text.
					plog(d, id, "codemap.round", fmt.Sprintf("[%s round %d] %s", turnID, ev.Round+1, excerpt2000(ev.Text)),
						map[string]any{"turnId": turnID, "round": ev.Round + 1})
				case "tool_start":
					step++
					toolStarts[ev.Tool+ev.Args] = time.Now()
					plog(d, id, "codemap.tool", fmt.Sprintf("[%s step %d] %s %s", turnID, step, ev.Tool, excerpt2000(ev.Args)),
						map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool})
				case "tool_done":
					started := toolStarts[ev.Tool+ev.Args]
					ms := int64(0)
					if !started.IsZero() {
						ms = time.Since(started).Milliseconds()
					}
					if ev.Err != "" {
						plog(d, id, "codemap.tool_error", fmt.Sprintf("[%s] %s failed in %dms: %s", turnID, ev.Tool, ms, excerpt2000(ev.Err)),
							map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "error": capData(ev.Err, 4000), "durationMs": ms})
					} else {
						// Tool output is persisted in the thread file; the
						// message carries a 2000-char excerpt. Storing the
						// full output here doubled ring memory per step.
						plog(d, id, "codemap.tool_result", fmt.Sprintf("[%s] %s done in %dms (%d chars): %s", turnID, ev.Tool, ms, len(ev.Result), excerpt2000(ev.Result)),
							map[string]any{"turnId": turnID, "step": step, "tool": ev.Tool, "chars": len(ev.Result), "durationMs": ms})
					}
				case "model_error":
					plog(d, id, "codemap.model_error", fmt.Sprintf("[%s] model=%s failed after %dms: %s", turnID, cfg.Model, time.Since(runStart).Milliseconds(), excerpt2000(ev.Err)),
						map[string]any{"turnId": turnID, "model": cfg.Model, "error": capData(ev.Err, 4000)})
				case "ref_drop":
					plog(d, id, "codemap.ref_dropped", fmt.Sprintf("[%s] ref dropped %s %s: %s", turnID, ev.Tool, excerpt2000(ev.Args), excerpt2000(ev.Result)),
						map[string]any{"turnId": turnID, "path": ev.Tool, "range": ev.Args, "reason": ev.Result})
				}
			})
		// Keep the complete debug graph separate from the FE thread record.
		// It is written on both success and failure so failed provider calls
		// remain diagnosable.
		if lineage != nil {
			lineage.TurnID = turnID
			lineage.ThreadID = threadID
			lineage.Prompt = prompt
			lineage.Time = time.Now().UTC()
			if err != nil {
				lineage.Error = err.Error()
			}
			if raw, merr := json.Marshal(lineage); merr == nil {
				// Lineage is named after the thread (prompt title) like the
				// thread file, with a .lineage.json suffix; the store keeps
				// it beside the thread and moves it on renames.
				if lerr := st.SaveLineage(id, threadID, threadTitle, raw); lerr != nil {
					plog(d, id, "codemap.lineage_error", fmt.Sprintf("[%s] lineage save failed: %s", turnID, lerr), map[string]any{"turnId": turnID, "error": lerr.Error()})
				} else {
					plog(d, id, "codemap.lineage_saved", fmt.Sprintf("[%s] lineage saved (%d bytes)", turnID, len(raw)), map[string]any{"turnId": turnID, "bytes": len(raw), "stage": "save_lineage"})
				}
			} else {
				plog(d, id, "codemap.lineage_error", fmt.Sprintf("[%s] lineage marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "error": merr.Error(), "stage": "marshal_lineage"})
			}
		}
		if err != nil {
			plog(d, id, "codemap.error", fmt.Sprintf("[%s] failed after %dms: %s", turnID, time.Since(runStart).Milliseconds(), excerpt2000(err.Error())),
				map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "error": capData(err.Error(), 4000), "durationMs": time.Since(runStart).Milliseconds()})
			if ctx.Err() != nil {
				writeCodemapErr(w, http.StatusGatewayTimeout, "codemap timed out", threadID, threadTitle)
			} else {
				writeCodemapErr(w, http.StatusBadGateway, err.Error(), threadID, threadTitle)
			}
			return
		}
		steps := flattenRounds(rounds)
		sections := res.Sections
		tools := steps
		// The thread file is both the record and the context source: one
		// chat per file, many chats per project. The file was created
		// before the run, so it survives failures. Tools persist as
		// rounds (laid out as the turn made them); the response flattens
		// steps for the Steps panel. events.log keeps only a lightweight
		// audit line — never the sections.
		sectionsRaw, merr := json.Marshal(res.Sections)
		if merr != nil {
			plog(d, id, "codemap.persistence_error", fmt.Sprintf("[%s] sections marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_sections", "error": merr.Error()})
			writeInternalErr(w, "marshal codemap sections", merr)
			return
		}
		toolsRaw, merr := json.Marshal(rounds)
		if merr != nil {
			plog(d, id, "codemap.persistence_error", fmt.Sprintf("[%s] tools marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_tools", "error": merr.Error()})
			writeInternalErr(w, "marshal codemap tools", merr)
			return
		}
		extractorOutputRaw, merr := json.Marshal(map[string]any{"result": res})
		if merr != nil {
			plog(d, id, "codemap.persistence_error", fmt.Sprintf("[%s] extractor output marshal failed: %s", turnID, merr), map[string]any{"turnId": turnID, "stage": "marshal_extractor_output", "error": merr.Error()})
			writeInternalErr(w, "marshal codemap output", merr)
			return
		}
		now := time.Now().UTC()
		th, err := st.AppendTurn(id, threadID, codemapthreads.Turn{
			TurnID: turnID, SHA: sha, Prompt: prompt,
			Sections: json.RawMessage(sectionsRaw), Tools: json.RawMessage(toolsRaw),
			ExtractorOutput: json.RawMessage(extractorOutputRaw),
			Time:            now,
		})
		if err != nil {
			plog(d, id, "codemap.persistence_error", fmt.Sprintf("[%s] turn append failed; thread remains without this turn: %s", turnID, err), map[string]any{"turnId": turnID, "threadId": threadID, "stage": "append_turn", "error": err.Error()})
			if err.Error() == "unknown thread" {
				writeUnknownThread(w)
				return
			}
			writeInternalErr(w, "append turn", err)
			return
		}
		plog(d, id, "codemap.turn_saved", fmt.Sprintf("[%s] thread turn persisted: title=%q sections=%d rounds=%d", turnID, th.Title, len(res.Sections), len(rounds)), map[string]any{"turnId": turnID, "threadId": threadID, "title": th.Title, "sections": len(res.Sections), "rounds": len(rounds), "stage": "append_turn"})
		_, _ = d.Events.Append("codemap.turn", map[string]any{
			"project": id, "threadId": threadID, "turnId": turnID,
			"prompt": capData(prompt, 500), "sections": len(res.Sections), "tools": len(steps),
		})
		plog(d, id, "codemap.done", fmt.Sprintf("[%s] done in %dms: %d section(s) [%s], %d tool call(s) [%s]",
			turnID, time.Since(runStart).Milliseconds(), len(res.Sections), sectionTitles(res.Sections), len(steps), toolNames(rounds)),
			// Sections and tools are persisted in the thread file and
			// returned in the response; duplicating them in the ring
			// ballooned log memory per turn.
			map[string]any{"turnId": turnID, "threadId": threadID, "model": cfg.Model, "sha": sha,
				"sections": len(res.Sections), "tools": len(steps), "durationMs": time.Since(runStart).Milliseconds()})
		writeJSON(w, http.StatusOK, map[string]any{
			"turnId": turnID, "sha": sha, "sections": sections, "tools": tools,
			"extractorOutput": map[string]any{"result": res},
			"threadId":        threadID, "threadTitle": th.Title,
			"time": now.Format(time.RFC3339),
		})
	}
}

// excerpt2000 is the roomier preview for codemap run messages: prompts,
// tool args/outputs, and errors stay readable without flooding the ring.
// Full text still lands in the entry data via capData.
func excerpt2000(s string) string {
	s = strings.TrimSpace(s)
	const max = 2000
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

// capData bounds a string stored in a log entry's data map.
func capData(s string, max int) string {
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func sectionTitles(sections []codemap.Section) string {
	titles := make([]string, 0, len(sections))
	for _, s := range sections {
		titles = append(titles, s.Title)
	}
	return strings.Join(titles, "; ")
}

func toolNames(rounds []codemap.ToolRound) string {
	var names []string
	for _, r := range rounds {
		for _, c := range r.Steps {
			names = append(names, c.Tool)
		}
	}
	return strings.Join(names, ", ")
}

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

// parseRounds decodes Turn.Tools in both shapes: current []ToolRound and
// legacy flat []ToolStep (treated as one round) or [{tool,args}].
func parseRounds(raw json.RawMessage) []codemap.ToolRound {
	if len(raw) == 0 {
		return nil
	}
	var rounds []codemap.ToolRound
	if err := json.Unmarshal(raw, &rounds); err == nil && len(rounds) > 0 {
		// Distinguish real rounds from a legacy flat step list: flat
		// steps unmarshal into rounds with empty Steps (unknown fields
		// dropped), so fall through when nothing parsed.
		hasSteps := false
		for _, r := range rounds {
			if len(r.Steps) > 0 || strings.TrimSpace(r.Thought) != "" {
				hasSteps = true
				break
			}
		}
		if hasSteps {
			return rounds
		}
	}
	var steps []codemap.ToolStep
	if err := json.Unmarshal(raw, &steps); err == nil && len(steps) > 0 {
		return []codemap.ToolRound{{Steps: steps}}
	}
	return nil
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
			if len(snip) > 200 {
				snip = snip[:200] + "…"
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

// threadHistory rebuilds the LLM conversation from the persisted thread:
// the thread file is both the history record and the context source.
// Each turn becomes user(prompt) [+ one assistant(toolSteps) per tool
// round, laid out as the turn made them + assistant(transcript)]. Old
// turns without outputs skip the tool block (grounding unrecoverable).
// Prior answers replay as a natural-language transcript, not raw JSON.
// Call IDs are not persisted; agent.Run assigns fresh global call_N ids.
// Context is bounded to the last 20 turns and ~16KB estimated chars.
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
		container, dir, ok := gitRepoDir(d, w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		path := q.Get("path")
		if !validGitPath(path) {
			writeErr(w, http.StatusBadRequest, "invalid path")
			return
		}
		start, serr := strconv.Atoi(q.Get("start"))
		end, eerr := strconv.Atoi(q.Get("end"))
		if serr != nil || eerr != nil || start < 1 || end < start || end-start > 119 {
			writeErr(w, http.StatusBadRequest, "start/end must span 1-120 lines with start >= 1")
			return
		}
		out, err := d.Sessions.ExecCommand(r.Context(), container,
			fmt.Sprintf("sed -n '%d,%dp' %s", start, end, shellQuote(dir+"/"+path)))
		if err != nil {
			// Missing file (deleted since generation) reads as empty, not 500.
			if strings.Contains(err.Error(), "exit ") && strings.TrimSpace(out) == "" {
				writeJSON(w, http.StatusOK, map[string]any{
					"path": path, "content": "", "binary": false, "moved": true,
					"sha": repoSHA(d, r, container, dir),
				})
				return
			}
			writeInternalErr(w, "read file", err)
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
			out = out[:100*1024]
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
