// Thread status: running > awaiting > failed > ready.
package httpapi

import "pcoder/internal/threads"

func threadStatus(running, awaiting, failed bool) string {
	if running {
		return "running"
	}
	if awaiting {
		return "awaiting"
	}
	if failed {
		return "failed"
	}
	return "ready"
}

func lastFailed(th threads.Thread) bool { return len(th.Turns) > 0 && th.Turns[len(th.Turns)-1].Error != nil }

func pendingApprovals(th threads.Thread) bool {
	for _, a := range th.Approvals {
		if a.Status == "" || a.Status == threads.ApprovalPending {
			return true
		}
	}
	return false
}

func runningFor(t *runTracker, key, tid string) bool {
	rid, busy := t.running(key)
	return busy && (rid == "" || rid == tid)
}

func butlerStatus(th threads.Thread) string {
	return threadStatus(runningFor(butlerRuns, butlerRunKey, th.ID), pendingApprovals(th), lastFailed(th))
}

func codemapStatus(project string, th threads.Thread) string {
	return threadStatus(runningFor(codemapRuns, project, th.ID), false, lastFailed(th))
}

func statusRows(summaries []threads.Summary, status func(id string) string) []any {
	rows := make([]any, 0, len(summaries))
	for _, s := range summaries {
		rows = append(rows, struct {
			threads.Summary
			Status string `json:"status"`
		}{s, status(s.ID)})
	}
	return rows
}
