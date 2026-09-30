// Shared turn plumbing both chat products ride: detach the run from the
// HTTP request so refresh never cancels it, reserve the placeholder turn
// before the model runs so failures stay retryable, and guard deletes
// against the in-flight thread only.
package httpapi

import (
	"context"
	"net/http"

	"pcoder/internal/threads"
)

// detached returns the request with a cancellation-independent context.
// Refresh aborts the client's fetch; the model loop keeps going and still
// persists via CompleteTurn.
func detached(r *http.Request) *http.Request {
	return r.WithContext(context.WithoutCancel(r.Context()))
}

// reservedTurn is one placeholder turn ready to run.
type reservedTurn struct {
	threadID string
	turnID   string
	title    string
	n        int
}

// reserveTurn creates the placeholder: new thread when threadID is empty,
// followup otherwise. Pending approvals (butler store) surface as
// threads.ErrPendingApproval; callers map that to 409.
func reserveTurn(st *threads.Store, scope, threadID, prompt, sha string) (reservedTurn, error) {
	if threadID == "" {
		tid, turn, err := st.ReserveNewThread(scope, prompt, sha, nil)
		if err != nil {
			return reservedTurn{}, err
		}
		return reservedTurn{threadID: tid, turnID: turn, title: threads.TitleFromPrompt(prompt), n: 1}, nil
	}
	th, err := st.Get(scope, threadID)
	if err != nil {
		return reservedTurn{}, err
	}
	n, turn, err := st.ReserveFollowup(scope, threadID, prompt, sha, nil)
	if err != nil {
		return reservedTurn{}, err
	}
	return reservedTurn{threadID: th.ID, turnID: turn, title: th.Title, n: n}, nil
}

// deleteBlocked reports whether deleting tid would pull the folder out
// from under a live run. Empty running id means reservation in flight:
// block conservatively since the thread is not known yet.
func deleteBlocked(t *runTracker, key, tid string) bool {
	return runningFor(t, key, tid)
}

// turnErrBody builds the failed-turn answer both products reply with:
// the message plus thread identity so the client opens the failed
// placeholder and retries instead of losing it.
func turnErrBody(msg, threadID, threadTitle string) map[string]any {
	body := map[string]any{"error": msg}
	if threadID != "" {
		body["threadId"] = threadID
	}
	if threadTitle != "" {
		body["threadTitle"] = threadTitle
	}
	return body
}
