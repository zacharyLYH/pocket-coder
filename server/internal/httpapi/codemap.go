// Codemap endpoints: ask-about-the-code over the agent loop, plus the
// read only file reader the snippet overlay consumes and the history
// reader that joins request/response event pairs on turn id.
//
// Tools are read only. No write path exists in this file.
package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/codemap"
)

// codemapBusy serializes one run per project: a second POST while one is
// in flight gets 409 codemap busy instead of burning a second loop.
var codemapBusy = struct {
	mu sync.Mutex
	m  map[string]bool
}{m: map[string]bool{}}

func codemapTake(id string) bool {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	if codemapBusy.m[id] {
		return false
	}
	codemapBusy.m[id] = true
	return true
}

func codemapDone(id string) {
	codemapBusy.mu.Lock()
	defer codemapBusy.mu.Unlock()
	delete(codemapBusy.m, id)
}

func mintTurnID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
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
			Prompt  string           `json:"prompt"`
			History []map[string]any `json:"history"`
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
		if !codemapTake(id) {
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		defer codemapDone(id)

		sha := repoSHA(d, r, container, dir)
		turnID := mintTurnID()
		plog(d, id, "codemap.start", prompt, map[string]any{"turnId": turnID})
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		res, calls, err := codemap.Ask(ctx, cfg, d.Sessions, container, dir, prompt, body.History,
			func(ev agent.TraceEvent) {
				switch ev.Kind {
				case "tool_start":
					plog(d, id, "codemap.tool", ev.Tool+" "+ev.Args, map[string]any{"turnId": turnID, "tool": ev.Tool, "args": ev.Args})
				case "tool_done":
					if ev.Err != "" {
						plog(d, id, "codemap.tool_error", ev.Tool+": "+ev.Err, map[string]any{"turnId": turnID, "tool": ev.Tool})
					} else {
						plog(d, id, "codemap.tool_result", ev.Tool+" → "+excerpt500(ev.Result), map[string]any{"turnId": turnID, "tool": ev.Tool})
					}
				case "model_error":
					plog(d, id, "codemap.model_error", ev.Err, map[string]any{"turnId": turnID})
				}
			})
		if err != nil {
			if ctx.Err() != nil {
				writeErr(w, http.StatusGatewayTimeout, "codemap timed out")
			} else {
				writeErr(w, http.StatusBadGateway, err.Error())
			}
			return
		}
		sections := toAny(res.Sections)
		tools := toAny(calls)
		_, _ = d.Events.Append("codemap.request", map[string]any{
			"project": id, "turnId": turnID, "sha": sha, "prompt": prompt,
		})
		_, _ = d.Events.Append("codemap.response", map[string]any{
			"project": id, "turnId": turnID, "sha": sha, "sections": sections, "tools": tools,
		})
		plog(d, id, "codemap.done", fmt.Sprintf("%d section(s), %d tool call(s)", len(res.Sections), len(calls)), map[string]any{"turnId": turnID})
		writeJSON(w, http.StatusOK, map[string]any{
			"turnId": turnID, "sha": sha, "sections": sections, "tools": tools,
		})
	}
}

func toAny(v any) any {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// excerpt500 caps tool output previews for the live log: enough to debug,
// small enough to keep the in-memory ring useful.
func excerpt500(s string) string {
	s = strings.TrimSpace(s)
	const max = 500
	if len(s) > max {
		return s[:max] + "…"
	}
	if s == "" {
		return "(empty)"
	}
	return s
}

func handleCodemapHistory(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		limit := 20
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, "limit must be a non-negative number")
				return
			}
			limit = n
		}
		if limit > 100 {
			limit = 100
		}
		evs, err := d.Events.Read(0, 0)
		if err != nil {
			writeInternalErr(w, "read events", err)
			return
		}
		type turn struct {
			TurnID   string `json:"turnId"`
			SHA      string `json:"sha"`
			Prompt   string `json:"prompt"`
			Sections any    `json:"sections"`
			Tools    any    `json:"tools,omitempty"`
			Time     string `json:"time,omitempty"`
			order    int64
		}
		reqs := map[string]*turn{}
		order := int64(0)
		for _, e := range evs {
			proj, _ := e.Data["project"].(string)
			if proj != id {
				continue
			}
			tid, _ := e.Data["turnId"].(string)
			if tid == "" {
				continue
			}
			switch e.Type {
			case "codemap.request":
				t := reqs[tid]
				if t == nil {
					order++
					t = &turn{TurnID: tid, order: order}
					reqs[tid] = t
				}
				t.Prompt, _ = e.Data["prompt"].(string)
				t.SHA, _ = e.Data["sha"].(string)
				t.Time = e.Time.Format(time.RFC3339)
			case "codemap.response":
				t := reqs[tid]
				if t == nil {
					order++
					t = &turn{TurnID: tid, order: order}
					reqs[tid] = t
				}
				t.Sections = e.Data["sections"]
				t.Tools = e.Data["tools"]
				if s, _ := e.Data["sha"].(string); s != "" && t.SHA == "" {
					t.SHA = s
				}
			}
		}
		ordered := make([]*turn, 0, len(reqs))
		for _, t := range reqs {
			if t.Prompt == "" {
				continue
			}
			ordered = append(ordered, t)
		}
		for i := 0; i < len(ordered); i++ {
			for j := i + 1; j < len(ordered); j++ {
				if ordered[j].order < ordered[i].order {
					ordered[i], ordered[j] = ordered[j], ordered[i]
				}
			}
		}
		if len(ordered) > limit {
			ordered = ordered[len(ordered)-limit:]
		}
		// order field is unexported so it stays out of the JSON.
		writeJSON(w, http.StatusOK, map[string]any{"turns": ordered})
	}
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
