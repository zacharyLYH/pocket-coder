// Codemap thread endpoints: one folder per thread (see codemapthreads),
// many chats per project, listed and reopened from the Codemap pane.
//
// events.log carries only a lightweight codemap.turn audit line per turn —
// never the sections themselves. The thread folder is the record.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/obs"
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

// handleCodemapThreads lists a project's chats, newest first (strictly by
// createdAt), plus the in-flight thread id for remount-into-run.
func handleCodemapThreads(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapThreads, "list threads failed", err, nil)
			}
		}()
		st, ok := threadsOr500(w, d)
		if !ok {
			err = errStoreUnconfigured
			return
		}
		summaries, lerr := st.List(id)
		if lerr != nil {
			err = lerr
			writeInternalErr(w, "list threads", lerr)
			return
		}
		if summaries == nil {
			summaries = []codemapthreads.Summary{}
		}
		var running any
		if tid, busy := codemapRunning(id); busy && tid != "" {
			running = tid
		} else {
			running = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{"threads": summaries, "runningThreadId": running})
	}
}

// handleCodemapThreadGet returns one chat with all its turns.
func handleCodemapThreadGet(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapThread, "get thread failed", err, map[string]any{"threadId": tid})
			}
		}()
		st, ok := threadsOr500(w, d)
		if !ok {
			err = errStoreUnconfigured
			return
		}
		th, gerr := st.Get(id, tid)
		if gerr != nil {
			err = errors.New("unknown thread")
			writeUnknownThread(w)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"thread": threadJSON(th)})
	}
}

// handleCodemapThreadDelete drops one chat folder. Idempotent when idle;
// 409 while that thread's generation is in flight so a run never loses
// the folder it is about to write to. Deleting an unrelated thread
// during a run stays allowed.
func handleCodemapThreadDelete(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		tid := r.PathValue("tid")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.CodemapThreadDeleted, "delete thread failed", err, map[string]any{"threadId": tid})
			}
		}()
		st, ok := threadsOr500(w, d)
		if !ok {
			err = errStoreUnconfigured
			return
		}
		if running, busy := codemapRunning(id); busy && running != "" && running == tid {
			err = errors.New("codemap busy — wait for the current run")
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		if derr := st.Delete(id, tid); derr != nil {
			err = derr
			writeInternalErr(w, "delete thread", derr)
			return
		}
		obs.Info(r.Context(), obs.CodemapThreadDeleted, "chat "+tid+" deleted", map[string]any{"threadId": tid})
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	}
}

// threadJSON renders a thread with turns shaped as CodemapTurn
// (turnId/sha/prompt/sections/tools/time/error) so the frontend reuses
// the type verbatim. No extractorOutput: N.json is the user-facing side.
// Sections default to [] (never null) so the client sees one shape.
func threadJSON(th codemapthreads.Thread) any {
	turns := make([]any, 0, len(th.Turns))
	for _, t := range th.Turns {
		var sections any = []any{}
		if len(t.Sections) > 0 {
			var decoded any
			if err := json.Unmarshal(t.Sections, &decoded); err == nil && decoded != nil {
				sections = decoded
			}
		}
		tools := flattenRounds(parseRounds(t.Tools))
		var turnErr any
		if t.Error != nil {
			turnErr = *t.Error
		}
		turns = append(turns, map[string]any{
			"turnId": t.TurnID, "sha": t.SHA, "prompt": t.Prompt,
			"sections": sections, "tools": tools, "error": turnErr,
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
