// Package httpapi wires the server's HTTP surface. Handlers live here, not
// in cmd/server, so main stays a thin bootstrap and the growing route set
// doesn't bloat it.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"sps/internal/auth"
	"sps/internal/docker"
	"sps/internal/events"
	"sps/internal/harness"
	"sps/internal/project"
	"sps/internal/session"
	"sps/internal/sshkeys"
)

// EventLog is what handlers need from the event log: read history and append
// new lines. *events.Log satisfies it; tests use a real temp log.
type EventLog interface {
	events.Reader
	Append(typ string, data map[string]any) (events.Event, error)
}

// Deps carries everything New needs. Grows over time instead of
// stretching New's signature.
type Deps struct {
	Events    EventLog
	Version   string
	Auth      *auth.Service
	Projects  *project.Service
	Sessions  *session.Service
	Harnesses *harness.Store
	SSHKeys   *sshkeys.Store
}

// New returns the HTTP handler for the whole server. Login/PIN routes are
// public; everything else under /api requires a valid session cookie.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	authed := func(method, path string, h func(Deps) http.HandlerFunc) {
		mux.Handle(method+" "+path, d.Auth.RequireAuth(h(d)))
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": d.Version})
	})

	mux.HandleFunc("POST /api/auth/request-pin", handleRequestPIN(d))
	mux.HandleFunc("POST /api/auth/verify", handleVerify(d))
	mux.HandleFunc("POST /api/auth/logout", handleLogout(d))
	authed("GET", "/api/auth/me", handleMe)
	authed("GET", "/api/events", func(d Deps) http.HandlerFunc { return handleEvents(d.Events) })

	if d.Projects != nil {
		authed("GET", "/api/projects", handleListProjects)
		authed("POST", "/api/projects", handleCreateProject)
		authed("GET", "/api/projects/{id}", handleGetProject)
		authed("DELETE", "/api/projects/{id}", handleDeleteProject)
		for _, op := range []string{"start", "stop", "restart"} {
			authed("POST", "/api/projects/{id}/"+op, func(d Deps) http.HandlerFunc { return handleProjectOp(d, op) })
		}
	}

	if d.Sessions != nil {
		authed("GET", "/api/projects/{id}/sessions", handleListSessions)
		authed("POST", "/api/projects/{id}/sessions", handleCreateSession)
		authed("DELETE", "/api/projects/{id}/sessions/{name}", handleKillSession)
		authed("POST", "/api/projects/{id}/sessions/{name}/restart", handleRestartSession)
		authed("GET", "/ws/projects/{id}/sessions/{name}", handleTerminal)
	}

	if d.Harnesses != nil && d.Projects != nil {
		authed("GET", "/api/projects/{id}/harnesses", handleProjectHarnesses)
		authed("POST", "/api/harnesses/{id}/install", handleInstallHarness)
	}

	if d.Projects != nil && d.Sessions != nil {
		authed("POST", "/api/projects/exec", handleExecCommand)
	}

	if d.Harnesses != nil {
		authed("GET", "/api/harnesses", handleListHarnesses)
		authed("POST", "/api/harnesses", handleCreateHarness)
		authed("DELETE", "/api/harnesses/{id}", handleDeleteHarness)
	}

	if d.SSHKeys != nil {
		authed("GET", "/api/ssh-keys", handleListSSHKeys)
		authed("POST", "/api/ssh-keys", handleAddSSHKey)
		authed("DELETE", "/api/ssh-keys/{fingerprint}", handleDeleteSSHKey)
	}
	return mux
}

func handleRequestPIN(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		body.Email = strings.TrimSpace(body.Email)
		err := d.Auth.RequestPIN(r.Context(), body.Email)
		switch {
		case errors.Is(err, auth.ErrNotConfiguredEmail):
			// Deliberately identical to success: don't reveal whether an
			// address is configured.
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case errors.Is(err, auth.ErrRateLimited):
			writeErr(w, http.StatusTooManyRequests, "too many requests, try again later")
		case err != nil:
			writeInternalErr(w, "request pin", err)
		default:
			d.Events.Append("login.pin.sent", map[string]any{"email": body.Email, "delivery": d.Auth.MailerName})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

func handleVerify(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Email string `json:"email"`
			Pin   string `json:"pin"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		token, err := d.Auth.Verify(strings.TrimSpace(body.Email), strings.TrimSpace(body.Pin))
		switch {
		case errors.Is(err, auth.ErrRateLimited):
			d.Events.Append("login.failure", map[string]any{"email": body.Email, "reason": "rate_limited"})
			writeErr(w, http.StatusTooManyRequests, "too many attempts, try again later")
		case errors.Is(err, auth.ErrInvalidPIN):
			d.Events.Append("login.failure", map[string]any{"email": body.Email, "reason": "invalid_pin"})
			writeErr(w, http.StatusUnauthorized, "invalid pin")
		case err != nil:
			writeInternalErr(w, "verify pin", err)
		default:
			d.Events.Append("login.success", map[string]any{"email": body.Email})
			d.Auth.SetCookie(w, r, token)
			writeJSON(w, http.StatusOK, map[string]any{"email": body.Email})
		}
	}
}

func handleLogout(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d.Auth.ClearCookie(w, r)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handleMe(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"email": d.Auth.Email(r)})
	}
}

// handleCreateProject runs the create pipeline synchronously: sandbox up,
// repo cloned (when given), ready.
func handleCreateProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RepoURL     string `json:"repoUrl"`
			Branch      string `json:"branch"`
			CloneMethod string `json:"cloneMethod"` // "ssh" or "http"
		}
		if r.Body != nil && !decodeBody(w, r, &body, true) {
			return
		}
		id, p, err := d.Projects.Create(r.Context(), strings.TrimSpace(body.RepoURL), strings.TrimSpace(body.Branch), strings.TrimSpace(body.CloneMethod))
		switch {
		case errors.Is(err, project.ErrInvalidInput):
			writeErr(w, http.StatusBadRequest, err.Error())
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusCreated, map[string]any{
				"id": id, "name": p.Name, "repo": p.Repo, "branch": p.Branch,
			})
		}
	}
}

func handleListProjects(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		entries, err := d.Projects.List()
		if err != nil {
			writeInternalErr(w, "list projects", err)
			return
		}
		if entries == nil {
			entries = []project.Entry{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"projects": entries})
	}
}

func handleGetProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, status, err := d.Projects.Get(r.Context(), r.PathValue("id"))
		switch {
		case errors.Is(err, project.ErrNotFound):
			writeErr(w, http.StatusNotFound, "no such project")
		case err != nil:
			writeInternalErr(w, "get project", err)
		default:
			writeJSON(w, http.StatusOK, map[string]any{
				"id": r.PathValue("id"), "name": p.Name, "repo": p.Repo,
				"branch": p.Branch, "cloneMethod": p.CloneMethod, "status": status.State,
			})
		}
	}
}

// handleProjectOp serves POST /{id}/start|stop|restart.
func handleProjectOp(d Deps, op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx := r.Context()
		var err error
		switch op {
		case "start":
			err = d.Projects.Start(ctx, id)
		case "stop":
			err = d.Projects.Stop(ctx, id)
		case "restart":
			err = d.Projects.Restart(ctx, id)
		}
		writeServiceErr(w, err)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

func handleDeleteProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scope := project.Scope(r.URL.Query().Get("scope"))
		if scope == "" {
			scope = project.ScopeAll
		}
		err := d.Projects.Delete(r.Context(), r.PathValue("id"), scope)
		switch {
		case err == nil:
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		case errors.Is(err, project.ErrInvalidScope):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			writeServiceErr(w, err)
		}
	}
}

// writeServiceErr maps service errors to statuses; on a mapped error it
// writes the response, otherwise it leaves it to the caller.
func writeServiceErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrNotFound):
		writeErr(w, http.StatusNotFound, "no such project")
	case errors.Is(err, docker.ErrNotFound):
		writeErr(w, http.StatusNotFound, "container not found")
	case err != nil:
		writeInternalErr(w, "project op", err)
	}
}

func handleEvents(ev events.Reader) http.HandlerFunc {
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
		limit := 100
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
		list, err := ev.Read(after, limit)
		if err != nil {
			writeInternalErr(w, "read events", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": list})
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr writes the standard JSON error shape {"error": msg}.
func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

const errInternal = "internal error"

// writeInternalErr logs op's failure and answers 500; the detail never
// reaches the client.
func writeInternalErr(w http.ResponseWriter, op string, err error) {
	slog.Error(op, "err", err)
	writeErr(w, http.StatusInternalServerError, errInternal)
}

// decodeBody decodes the JSON request body into v; on failure it writes the
// standard 400 and returns false. tolerateEOF allows an empty body (used by
// create-project, whose body is all-optional).
func decodeBody(w http.ResponseWriter, r *http.Request, v any, tolerateEOF bool) bool {
	err := json.NewDecoder(r.Body).Decode(v)
	if err != nil && !(tolerateEOF && errors.Is(err, io.EOF)) {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}
