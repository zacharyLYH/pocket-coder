package httpapi

import (
	"net/http"
	"strconv"
	"strings"
)

// Project running-log endpoints: the read/write surface over the
// per-project singleton (internal/projectlog). Deliberately unrelated to
// stdout and events.log — server code chooses exactly what lands here, and
// the Logs tab tails it per project.

func handleProjectLogs(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		if v := r.URL.Query().Get("after"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "after must be a number")
				return
			}
			after = n
		}
		limit := 200
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, "limit must be a non-negative number")
				return
			}
			limit = n
		}
		if limit > 1000 {
			limit = 1000
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": d.ProjectLogs.Read(r.PathValue("id"), after, limit)})
	}
}

func handleAppendProjectLog(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Type    string         `json:"type"`
			Message string         `json:"message"`
			Data    map[string]any `json:"data"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.Type = strings.TrimSpace(body.Type)
		body.Message = strings.TrimSpace(body.Message)
		if body.Type == "" || body.Message == "" {
			writeErr(w, http.StatusBadRequest, "type and message must be non-empty")
			return
		}
		e := d.ProjectLogs.Append(r.PathValue("id"), body.Type, body.Message, body.Data)
		writeJSON(w, http.StatusCreated, map[string]any{"log": e})
	}
}

// plog records one line in the project's running log — and nothing else.
// d.ProjectLogs is nil-safe, so callers never guard.
func plog(d Deps, projectID, typ, message string, data map[string]any) {
	d.ProjectLogs.Append(projectID, typ, message, data)
}
