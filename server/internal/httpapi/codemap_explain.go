// Fire-and-forget codemap runs triggered from the Git tab. The run
// detaches from the HTTP request (context.Background + own goroutine);
// surfacing rides the existing busy-slot bookkeeping (runningThreadId →
// CodemapTab remount-into-run). Thread + turn placeholder are persisted
// synchronously so a crash still leaves a retryable turn.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"pcoder/internal/codemapthreads"
	"pcoder/internal/obs"
)

const explainMaxPrompt = 60000

// explainPrompt builds the explainer prompt: a thorough high-level
// walkthrough with the (capped) diff embedded as context.
func explainPrompt(diff string, truncated bool) string {
	var sb strings.Builder
	sb.WriteString("Give a thorough, high-level walkthrough of these changes: what they do, why they look intended, and anything risky. Plain prose, no preamble.")
	sb.WriteString("\n\nDiff:\n\n")
	sb.WriteString(diff)
	if truncated {
		sb.WriteString("\n\n(the diff was truncated — note that in the walkthrough)")
	}
	return sb.String()
}

func handleGitExplain(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.GitExplain, "git explain failed", err, nil)
			}
		}()
		var body struct {
			Mode string `json:"mode"`
			Sha  string `json:"sha"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if body.Mode == "" {
			body.Mode = "working-tree"
		}
		if body.Mode != "working-tree" && body.Mode != "commit" {
			err = errors.New("mode must be working-tree or commit")
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		cfg, container, dir, serr := aiGitSetup(d, w, r)
		if serr != nil {
			err = serr
			return
		}
		ctx := r.Context()
		qd := shellQuote(dir)
		var diff string
		var truncated bool
		switch body.Mode {
		case "commit":
			sha := strings.TrimSpace(body.Sha)
			if !prShaRe.MatchString(sha) {
				err = errors.New("mode=commit requires a valid sha")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			if !commitExists(ctx, d, container, dir, sha) {
				err = errors.New("unknown commit")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			diff, _ = d.Sessions.ExecCommand(ctx, container,
				"git -C "+qd+" show "+shellQuote(sha)+" --format= -U3; true")
			diff, truncated = truncateDiff(diff)
		default:
			diff, truncated = cappedDiff(ctx, d, container, dir, true)
			if strings.TrimSpace(diff) == "" {
				err = errors.New("working tree is clean — nothing to explain")
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		prompt := explainPrompt(diff, truncated)
		if len(prompt) > explainMaxPrompt {
			prompt = prompt[:explainMaxPrompt]
		}

		st := d.Codemaps
		if st == nil {
			err = errStoreUnconfigured
			writeInternalErr(w, "codemap store", err)
			return
		}
		if !codemapTake(id, "") {
			obs.Info(ctx, obs.CodemapBusy, "explain rejected: previous run still in flight", nil)
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		// Thread + turn 1 placeholder persist synchronously: a crash
		// still leaves a retryable failed turn (same as the sync path).
		sha := repoSHA(d, r, container, dir)
		threadID, turnID, rerr := st.ReserveNewThread(id, prompt, sha)
		if rerr != nil {
			codemapDone(id)
			err = rerr
			writeInternalErr(w, "reserve explain thread", rerr)
			return
		}
		// Point the busy slot at the new thread id BEFORE spawning, so
		// GET /codemap/threads reports runningThreadId and CodemapTab's
		// remount-into-run effect opens the thread.
		codemapSet(id, threadID)
		threadTitle := codemapthreads.TitleFromPrompt(prompt)
		obs.Info(ctx, obs.GitExplain, "explain run started: "+threadID,
			map[string]any{"threadId": threadID, "turnId": turnID, "mode": body.Mode})

		// Detached run: request-independent deps only (store, sessions,
		// obs, events). Nothing pins the dying request.
		go func() {
			defer codemapDone(id)
			defer func() {
				if rec := recover(); rec != nil {
					obs.Error(obs.WithProject(context.Background(), id), obs.CodemapTurn,
						"explain run panicked: "+fmt.Sprint(rec),
						map[string]any{"threadId": threadID, "stage": "panic"})
					// Persist the failed turn so it stays retryable.
					if ferr := st.FailTurn(id, threadID, 1, codemapthreads.Turn{
						TurnID: turnID, SHA: sha, Prompt: prompt,
						Time: time.Now().UTC(),
					}, fmt.Sprint(rec), nil); ferr != nil {
						obs.Error(context.Background(), obs.CodemapTurn, "explain panic persist failed: "+ferr.Error(), nil)
					}
				}
			}()
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			// Synthetic request carrying only the detached context —
			// executeReservedTurn reads ctx (never body/writer until the
			// response sink, which is a no-op here).
			bgReq, _ := http.NewRequestWithContext(bgCtx, http.MethodPost, "/", nil)
			bgReq = bgReq.WithContext(obs.WithProject(bgCtx, id))
			executeReservedTurn(d, noopWriter{header: http.Header{}}, bgReq, id, container, dir, cfg, st,
				threadID, threadTitle, 1, turnID, prompt, nil)
		}()

		writeJSON(w, http.StatusAccepted, map[string]any{
			"threadId": threadID, "threadTitle": threadTitle,
		})
	}
}

// noopWriter absorbs the response writes of a background turn. The
// background path persists via CompleteTurn/FailTurn; the HTTP response
// is meaningless once the 202 has been sent.
type noopWriter struct {
	header http.Header
}

func (nw noopWriter) Header() http.Header         { return nw.header }
func (nw noopWriter) Write(p []byte) (int, error) { return len(p), nil }
func (nw noopWriter) WriteHeader(int)             {}
