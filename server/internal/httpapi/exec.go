// Explicit, synchronous command execution across selected projects — the
// home-page orchestration point. Harness installs and arbitrary commands
// (upgrades, maintenance) share one runner: the user picks the projects,
// the work happens now, and every project's outcome comes back.
package httpapi

import (
	"net/http"

	"pcoder/internal/project"
)

// execResult is one project's outcome in a batch run.
type execResult struct {
	Project string `json:"project"`
	Status  string `json:"status"` // "ok" | "skipped" | "error"
	Detail  string `json:"detail,omitempty"`
}

type batchExecBody struct {
	ProjectIDs []string `json:"projectIds"`
	Command    string   `json:"command"`
}

// runInProjects executes run in each named project's container, skipping
// stopped ones. Best-effort per project: one failure never stops the rest.
func runInProjects(d Deps, r *http.Request, ids []string, run func(container string) (string, error)) []execResult {
	byID := map[string]project.Entry{}
	if entries, err := d.Projects.List(); err == nil {
		for _, e := range entries {
			byID[e.ID] = e
		}
	}
	results := make([]execResult, 0, len(ids))
	for _, id := range ids {
		if _, known := byID[id]; !known {
			results = append(results, execResult{Project: id, Status: "error", Detail: "no such project"})
			continue
		}
		res := execResult{Project: id}
		st, cerr := d.Projects.EnsureContainer(r.Context(), id)
		switch {
		case cerr != nil:
			res.Status, res.Detail = "error", cerr.Error()
		case st.State != project.StateRunning:
			res.Status, res.Detail = "skipped", "container not running"
		default:
			if out, rerr := run(project.ContainerName(id)); rerr != nil {
				res.Status, res.Detail = "error", rerr.Error()
			} else {
				res.Status, res.Detail = "ok", out
			}
		}
		results = append(results, res)
	}
	return results
}

// handleExecCommand runs an arbitrary command in the selected projects.
// This is the general orchestration primitive — harness installs are a
// dedicated endpoint on top of the same machinery.
func handleExecCommand(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body batchExecBody
		if !decodeBody(w, r, &body, false) {
			return
		}
		if len(body.ProjectIDs) == 0 {
			writeErr(w, http.StatusBadRequest, "select at least one project")
			return
		}
		if body.Command == "" {
			writeErr(w, http.StatusBadRequest, "command is required")
			return
		}
		results := runInProjects(d, r, body.ProjectIDs, func(container string) (string, error) {
			return d.Sessions.ExecCommand(r.Context(), container, body.Command)
		})
		_, _ = d.Events.Append("projects.exec", map[string]any{"command": body.Command, "results": results})
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}
