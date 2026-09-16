// Codemap thread endpoints: one chat per file (see codemapthreads), many
// chats per project, listed and reopened from the Codemap pane.
//
// events.log carries only a lightweight codemap.turn audit line per turn —
// never the sections themselves. The thread file is the record.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"pcoder/internal/codemapthreads"
)

// errStoreUnconfigured surfaces a missing thread store as a 500.
var errStoreUnconfigured = errors.New("codemap store not configured")

// threadsOr500 resolves the thread store or answers 500. Every handler
// starts with it, so none repeats the nil check.
func threadsOr500(w http.ResponseWriter, d Deps) (*codemapthreads.Store, bool) {
	if d.Codemaps == nil {
		writeInternalErr(w, "codemap store", errStoreUnconfigured)
		return nil, false
	}
	return d.Codemaps, true
}

func writeUnknownThread(w http.ResponseWriter) {
	writeErr(w, http.StatusNotFound, "unknown thread")
}

// handleCodemapThreads lists a project's chats, newest first.
func handleCodemapThreads(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		st, ok := threadsOr500(w, d)
		if !ok {
			return
		}
		summaries, err := st.List(id)
		if err != nil {
			writeInternalErr(w, "list threads", err)
			return
		}
		if summaries == nil {
			summaries = []codemapthreads.Summary{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"threads": summaries})
	}
}

// handleCodemapThreadCreate starts an empty chat.
func handleCodemapThreadCreate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		st, ok := threadsOr500(w, d)
		if !ok {
			return
		}
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		th, err := st.Create(id, body.Title)
		if err != nil {
			writeInternalErr(w, "create thread", err)
			return
		}
		plog(d, id, "codemap.thread_created", "new chat "+th.ID, map[string]any{"threadId": th.ID})
		writeJSON(w, http.StatusCreated, map[string]any{"thread": threadJSON(th)})
	}
}

// handleCodemapThreadGet returns one chat with all its turns.
func handleCodemapThreadGet(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		st, ok := threadsOr500(w, d)
		if !ok {
			return
		}
		th, err := st.Get(id, tid)
		if err != nil {
			writeUnknownThread(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread": threadJSON(th)})
	}
}

// handleCodemapThreadRename sets a chat's title.
func handleCodemapThreadRename(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		st, ok := threadsOr500(w, d)
		if !ok {
			return
		}
		var body struct {
			Title string `json:"title"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if strings.TrimSpace(body.Title) == "" {
			writeErr(w, http.StatusBadRequest, "title is required")
			return
		}
		th, err := st.Rename(id, tid, body.Title)
		if err != nil {
			if err.Error() == "unknown thread" {
				writeUnknownThread(w)
				return
			}
			writeInternalErr(w, "rename thread", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread": threadJSON(th)})
	}
}

// handleCodemapThreadDelete drops one chat (its per-thread log file).
// Idempotent when idle; 409 while that thread's generation is in flight
// so a run never loses the file it is about to append to. Deleting an
// unrelated thread during a run stays allowed.
func handleCodemapThreadDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		st, ok := threadsOr500(w, d)
		if !ok {
			return
		}
		if running, busy := codemapRunning(id); busy && running != "" && running == tid {
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		if err := st.Delete(id, tid); err != nil {
			writeInternalErr(w, "delete thread", err)
			return
		}
		plog(d, id, "codemap.thread_deleted", "chat "+tid+" deleted", map[string]any{"threadId": tid})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// threadJSON renders a thread with turns shaped as CodemapTurn
// (turnId/sha/prompt/sections/tools/time) so the frontend reuses the
// type verbatim.
func threadJSON(th codemapthreads.Thread) any {
	turns := make([]any, 0, len(th.Turns))
	for _, t := range th.Turns {
		var sections, extractorOutput any
		if len(t.Sections) > 0 {
			_ = json.Unmarshal(t.Sections, &sections)
		}
		tools := flattenRounds(parseRounds(t.Tools))
		if len(t.ExtractorOutput) > 0 {
			_ = json.Unmarshal(t.ExtractorOutput, &extractorOutput)
		}
		turns = append(turns, map[string]any{
			"turnId": t.TurnID, "sha": t.SHA, "prompt": t.Prompt,
			"sections": sections, "tools": tools, "extractorOutput": extractorOutput,
			"time": t.Time.Format(time.RFC3339),
		})
	}
	return map[string]any{
		"id": th.ID, "project": th.Project, "title": th.Title,
		"createdAt": th.CreatedAt.Format(time.RFC3339),
		"updatedAt": th.UpdatedAt.Format(time.RFC3339),
		"turns":     turns,
	}
}
