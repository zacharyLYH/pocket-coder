// Fire-and-forget codemap runs triggered from the Git tab. The run
// detaches from the HTTP request (context.Background + own goroutine);
// surfacing rides the existing busy-slot bookkeeping (per-row status →
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

	"pcoder/internal/threads"
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
		if !codemapRuns.take(id, "") {
			obs.Info(ctx, obs.CodemapBusy, "explain rejected: previous run still in flight", nil)
			writeErr(w, http.StatusConflict, "codemap busy — wait for the current run")
			return
		}
		// Thread + turn 1 placeholder persist synchronously: a crash
		// still leaves a retryable failed turn (same as the sync path).
		sha := repoSHA(r.Context(), d, container, dir)
		threadID, turnID, rerr := st.ReserveNewThread(id, prompt, sha, nil)
		if rerr != nil {
			codemapRuns.done(id)
			err = rerr
			writeInternalErr(w, "reserve explain thread", rerr)
			return
		}
		// Point the busy slot at the new thread id BEFORE spawning, so
		// GET /codemap/threads reports the row as running and CodemapTab's
		// remount-into-run effect opens the thread.
		codemapRuns.set(id, threadID)
		threadTitle := threads.TitleFromPrompt(prompt)
		obs.Info(ctx, obs.GitExplain, "explain run started: "+threadID,
			map[string]any{"threadId": threadID, "turnId": turnID, "mode": body.Mode})

		// Detached run: a plain context, never the dying request. The
		// outcome is persisted via CompleteTurn; the HTTP answer went out
		// as 202 above, so the returned body is dropped.
		go func() {
			defer codemapRuns.done(id)
			defer func() {
				if rec := recover(); rec != nil {
					obs.Error(obs.WithProject(context.Background(), id), obs.CodemapTurn,
						"explain run panicked: "+fmt.Sprint(rec),
						map[string]any{"threadId": threadID, "stage": "panic"})
					// Persist the failed turn so it stays retryable.
					msg := fmt.Sprint(rec)
					if ferr := st.CompleteTurn(id, threadID, 1, threads.Turn{
						TurnID: turnID, SHA: sha, Prompt: prompt,
						Time: time.Now().UTC(), Error: &msg,
					}, nil); ferr != nil {
						obs.Error(context.Background(), obs.CodemapTurn, "explain panic persist failed: "+ferr.Error(), nil)
					}
				}
			}()
			bgCtx, cancel := context.WithTimeout(obs.WithProject(context.Background(), id), 10*time.Minute)
			defer cancel()
			runCodemapTurn(bgCtx, d, st, id, container, dir, cfg,
				threadID, threadTitle, 1, turnID, prompt, nil)
		}()

		writeJSON(w, http.StatusAccepted, map[string]any{
			"threadId": threadID, "threadTitle": threadTitle,
		})
	}
}
