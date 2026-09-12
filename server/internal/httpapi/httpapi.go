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
	"os"

	"pcoder/internal/auth"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/projectlog"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
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
	Events      EventLog
	Version     string
	Auth        *auth.Service
	Projects    *project.Service
	Preview     *preview.Manager
	ProjectLogs *projectlog.Manager
	Sessions    *session.Service
	Harnesses   *harness.Store
	SSHKeys     *sshkeys.Store
	State       *state.Store
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
	// Project running logs: ungated (nil-safe store) and existence-unchecked
	// — an unknown project simply tails empty.
	authed("GET", "/api/projects/{id}/logs", handleProjectLogs)
	authed("POST", "/api/projects/{id}/logs", handleAppendProjectLog)

	if d.Projects != nil {
		authed("GET", "/api/projects", handleListProjects)
		authed("POST", "/api/projects", handleCreateProject)
		authed("GET", "/api/projects/{id}", handleGetProject)
		authed("PATCH", "/api/projects/{id}", handlePatchProject)
		authed("DELETE", "/api/projects/{id}", handleDeleteProject)
		for _, op := range []string{"start", "stop", "restart"} {
			authed("POST", "/api/projects/{id}/"+op, func(d Deps) http.HandlerFunc { return handleProjectOp(d, op) })
		}
	}

	if d.Sessions != nil {
		authed("GET", "/api/projects/{id}/sessions", handleListSessions)
		authed("POST", "/api/projects/{id}/sessions", handleCreateSession)
		authed("DELETE", "/api/projects/{id}/sessions/{name}", handleKillSession)
		authed("DELETE", "/api/projects/{id}/sessions/{name}/delete", handleDeleteSession)
		authed("POST", "/api/projects/{id}/sessions/{name}/restart", handleRestartSession)
		authed("POST", "/api/projects/{id}/sessions/{name}/rename", handleRenameSession)
		authed("POST", "/api/projects/{id}/sessions/{name}/inject", handleInjectSession)
		authed("GET", "/ws/projects/{id}/sessions/{name}", handleTerminal)
	}

	if d.Harnesses != nil && d.Projects != nil {
		authed("GET", "/api/projects/{id}/harnesses", handleProjectHarnesses)
		authed("POST", "/api/harnesses/{id}/install", handleInstallHarness)
	}

	if d.Projects != nil && d.Sessions != nil {
		authed("POST", "/api/projects/exec", handleExecCommand)
		authed("GET", "/api/projects/{id}/git/status", handleGitStatus)
		authed("GET", "/api/projects/{id}/git/diff", handleGitDiff)
		authed("POST", "/api/projects/{id}/git/stage", func(d Deps) http.HandlerFunc { return handleGitStage(d, false) })
		authed("POST", "/api/projects/{id}/git/unstage", func(d Deps) http.HandlerFunc { return handleGitStage(d, true) })
		authed("POST", "/api/projects/{id}/git/stage-hunk", handleGitStageHunk)
	}

	if d.Preview != nil {
		authed("GET", "/api/projects/{id}/preview", handlePreviewStatus)
		authed("POST", "/api/projects/{id}/preview/start", handlePreviewStart)
		authed("DELETE", "/api/projects/{id}/preview", handlePreviewClose)
		authed("GET", "/api/projects/{id}/preview/{path...}", handlePreviewSurface)
	}
	if d.Preview != nil && d.Sessions != nil {
		authed("GET", "/api/projects/{id}/preview/ports", handlePreviewPorts)
		authed("GET", "/api/projects/{id}/preview/tools/screenshot", handlePreviewScreenshot)
		authed("GET", "/api/projects/{id}/preview/tools/inspect", handlePreviewInspect)
		authed("GET", "/api/projects/{id}/preview/tools/console", handlePreviewConsole)
		authed("GET", "/api/projects/{id}/preview/tools/network", handlePreviewNetwork)
		authed("POST", "/api/projects/{id}/preview/tools/navigate", handlePreviewNavigate)
		authed("POST", "/api/projects/{id}/preview/tools/click", handlePreviewClick)
		authed("POST", "/api/projects/{id}/preview/tools/type", handlePreviewType)
		authed("POST", "/api/projects/{id}/preview/tools/reload", handlePreviewReload)
		authed("POST", "/api/projects/{id}/preview/tools/scroll", handlePreviewScroll)
		authed("POST", "/api/projects/{id}/preview/tools/viewport", handlePreviewViewport)
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

	if d.State != nil {
		authed("GET", "/api/state", handleGetState)
	}
	return mux
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

// handleGetState dumps state.json plainly to the caller. The file is the
// single source of truth; this endpoint exists so large frontend e2e tests
// can assert desired state without reaching into the container's filesystem.
func handleGetState(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(d.State.Path())
		if err != nil {
			writeInternalErr(w, "read state", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}
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
