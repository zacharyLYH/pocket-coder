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
	"pcoder/internal/threads"

	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/obs"
	"pcoder/internal/preview"
	"pcoder/internal/project"
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
	Events    EventLog
	Version   string
	Auth      *auth.Service
	Projects  *project.Service
	Preview   *preview.Manager
	Obs       *obs.Store
	Docker    docker.Client
	Sessions  *session.Service
	Harnesses *harness.Store
	SSHKeys   *sshkeys.Store
	State     *state.Store
	Codemaps  *threads.Store
	Butler    *threads.Store
}

// New returns the HTTP handler for the whole server. Login/PIN routes are
// public; everything else under /api requires a valid session cookie.
func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	authed := func(method, path string, h func(Deps) http.HandlerFunc) {
		mux.Handle(method+" "+path, d.Auth.RequireAuth(h(d)))
	}
	// authedProject is authed plus project-id injection: every route
	// registered here carries the project id as {id}, so the id is baked
	// into ctx once and all downstream obs.Info/Error calls inherit it.
	// Routes without a project {id} stay on authed and inject (if ever)
	// in the handler itself:
	//   - POST /api/projects (create: the id doesn't exist yet; the
	//     service injects after parsing the repo URL),
	//   - POST /api/projects/exec and POST /api/harnesses/{id}/install
	//     (multi-project fan-out over body.projectIds; {id} there is a
	//     harness id, not a project),
	//   - home-page/global routes (list projects/harnesses, ssh-keys,
	//     ai config, auth, events, state) which have no single project.
	authedProject := func(method, path string, h func(Deps) http.HandlerFunc) {
		mux.Handle(method+" "+path, d.Auth.RequireAuth(withProjectID(h(d))))
	}

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": d.Version})
	})

	mux.HandleFunc("POST /api/auth/request-pin", handleRequestPIN(d))
	mux.HandleFunc("POST /api/auth/verify", handleVerify(d))
	mux.HandleFunc("POST /api/auth/logout", handleLogout(d))
	authed("GET", "/api/auth/me", handleMe)
	authed("GET", "/api/events", func(d Deps) http.HandlerFunc { return handleEvents(d.Events) })
	authed("GET", "/api/observe/meta", handleObserveMeta)
	authedProject("GET", "/api/projects/{id}/observe", handleObserve)
	authedProject("GET", "/api/projects/{id}/observe/stats", handleObserveStats)
	authedProject("GET", "/api/projects/{id}/observe/errors", handleObserveErrors)

	if d.Projects != nil {
		authed("GET", "/api/projects", handleListProjects)
		authed("POST", "/api/projects", handleCreateProject)
		authedProject("GET", "/api/projects/{id}", handleGetProject)
		authedProject("PATCH", "/api/projects/{id}", handlePatchProject)
		authedProject("DELETE", "/api/projects/{id}", handleDeleteProject)
		for _, op := range []string{"start", "stop", "restart"} {
			authedProject("POST", "/api/projects/{id}/"+op, func(d Deps) http.HandlerFunc { return handleProjectOp(d, op) })
		}
	}

	if d.Sessions != nil {
		authedProject("GET", "/api/projects/{id}/sessions", handleListSessions)
		authedProject("POST", "/api/projects/{id}/sessions", handleCreateSession)
		authedProject("DELETE", "/api/projects/{id}/sessions/{name}", handleKillSession)
		authedProject("DELETE", "/api/projects/{id}/sessions/{name}/delete", handleDeleteSession)
		authedProject("POST", "/api/projects/{id}/sessions/{name}/restart", handleRestartSession)
		authedProject("POST", "/api/projects/{id}/sessions/{name}/rename", handleRenameSession)
		authedProject("POST", "/api/projects/{id}/sessions/{name}/inject", handleInjectSession)
		authedProject("GET", "/ws/projects/{id}/sessions/{name}", handleTerminal)
	}

	if d.Harnesses != nil && d.Projects != nil {
		authedProject("GET", "/api/projects/{id}/harnesses", handleProjectHarnesses)
		authed("POST", "/api/harnesses/{id}/install", handleInstallHarness)
		authed("POST", "/api/harnesses/{id}/update-check", handleHarnessUpdateCheck)
	}

	if d.Projects != nil && d.Sessions != nil {
		authed("POST", "/api/projects/exec", handleExecCommand)
		authedProject("GET", "/api/projects/{id}/git/status", handleGitStatus)
		authedProject("GET", "/api/projects/{id}/git/diff", handleGitDiff)
		authedProject("POST", "/api/projects/{id}/git/stage", func(d Deps) http.HandlerFunc { return handleGitStage(d, false) })
		authedProject("POST", "/api/projects/{id}/git/unstage", func(d Deps) http.HandlerFunc { return handleGitStage(d, true) })
		authedProject("POST", "/api/projects/{id}/git/stage-hunk", handleGitStageHunk)
		authedProject("POST", "/api/projects/{id}/git/commit", handleGitCommit)
		authedProject("GET", "/api/projects/{id}/git/identity", handleGitIdentity)
		authedProject("POST", "/api/projects/{id}/git/identity", handleGitIdentity)
		authedProject("POST", "/api/projects/{id}/git/push", handleGitPush)
		authedProject("POST", "/api/projects/{id}/git/pull", handleGitPull)
		authedProject("GET", "/api/projects/{id}/git/branches", handleGitBranches)
		authedProject("POST", "/api/projects/{id}/git/switch", handleGitSwitch)
		authedProject("POST", "/api/projects/{id}/git/commit-message", handleGitCommitMessage)
		authedProject("POST", "/api/projects/{id}/git/pr-body", handleGitPRBody)
		authedProject("POST", "/api/projects/{id}/git/ops-prompt", handleGitOpsPrompt)
		authedProject("POST", "/api/projects/{id}/git/explain", handleGitExplain)
		authedProject("POST", "/api/projects/{id}/codemap", handleCodemap)
		authedProject("GET", "/api/projects/{id}/codemap/threads", handleCodemapThreads)
		authedProject("GET", "/api/projects/{id}/codemap/threads/{tid}", handleCodemapThreadGet)
		authedProject("DELETE", "/api/projects/{id}/codemap/threads/{tid}", handleCodemapThreadDelete)
		authedProject("POST", "/api/projects/{id}/codemap/threads/{tid}/retry", handleCodemapRetry)
		authedProject("GET", "/api/projects/{id}/file", handleCodemapFile)
	}

	if d.State != nil {
		authed("GET", "/api/ai/models", handleListAIModels)
		authed("POST", "/api/ai/models", handleCreateAIModel)
		authed("POST", "/api/ai/models/test", handleTestAIModelBody)
		authed("PUT", "/api/ai/models/{id}", handleUpdateAIModel)
		authed("DELETE", "/api/ai/models/{id}", handleDeleteAIModel)
		authed("POST", "/api/ai/models/{id}/test", handleTestAIModel)
	}

	if d.Preview != nil {
		authedProject("GET", "/api/projects/{id}/preview", withSource(obs.SourcePreview, handlePreviewStatus))
		authedProject("POST", "/api/projects/{id}/preview/start", withSource(obs.SourcePreview, handlePreviewStart))
		authedProject("DELETE", "/api/projects/{id}/preview", withSource(obs.SourcePreview, handlePreviewClose))
		authedProject("POST", "/api/projects/{id}/preview/heartbeat", withSource(obs.SourcePreview, handlePreviewHeartbeat))
		authedProject("GET", "/api/projects/{id}/preview/{path...}", withSource(obs.SourcePreview, handlePreviewSurface))
	}
	if d.Preview != nil && d.Sessions != nil {
		authedProject("GET", "/api/projects/{id}/preview/ports", withSource(obs.SourcePreview, handlePreviewPorts))
		authedProject("GET", "/api/projects/{id}/preview/tools/screenshot", withSource(obs.SourcePreview, handlePreviewScreenshot))
		authedProject("GET", "/api/projects/{id}/preview/tools/inspect", withSource(obs.SourcePreview, handlePreviewInspect))
		authedProject("GET", "/api/projects/{id}/preview/tools/console", withSource(obs.SourcePreview, handlePreviewConsole))
		authedProject("GET", "/api/projects/{id}/preview/tools/network", withSource(obs.SourcePreview, handlePreviewNetwork))
		authedProject("POST", "/api/projects/{id}/preview/tools/navigate", withSource(obs.SourcePreview, handlePreviewNavigate))
		authedProject("POST", "/api/projects/{id}/preview/tools/click", withSource(obs.SourcePreview, handlePreviewClick))
		authedProject("POST", "/api/projects/{id}/preview/tools/type", withSource(obs.SourcePreview, handlePreviewType))
		authedProject("POST", "/api/projects/{id}/preview/tools/reload", withSource(obs.SourcePreview, handlePreviewReload))
		authedProject("POST", "/api/projects/{id}/preview/tools/scroll", withSource(obs.SourcePreview, handlePreviewScroll))
		authedProject("POST", "/api/projects/{id}/preview/tools/viewport", withSource(obs.SourcePreview, handlePreviewViewport))
	}

	if d.Harnesses != nil {
		authed("GET", "/api/harnesses", handleListHarnesses)
		authed("POST", "/api/harnesses", handleCreateHarness)
		authed("DELETE", "/api/harnesses/{id}", handleDeleteHarness)
	}

	if d.SSHKeys != nil {
		authed("GET", "/api/ssh", handleServerKey)
		authed("POST", "/api/ssh/test", handleServerKeyTest)
		authed("POST", "/api/ssh/regenerate", handleServerKeyRegen)
	}

	if d.State != nil {
		authed("GET", "/api/state", handleGetState)
		authed("DELETE", "/api/state", handleDeleteState)
	}

	// Butler: one global history (no project scope). Turns are plain
	// JSON: reserve the placeholder, run detached, then answer.
	if d.Butler != nil {
		authed("GET", "/api/butler/threads", handleButlerThreads)
		authed("GET", "/api/butler/threads/{tid}", handleButlerThreadGet)
		authed("DELETE", "/api/butler/threads/{tid}", handleButlerThreadDelete)
		authed("POST", "/api/butler/turn", handleButlerTurn)
		authed("POST", "/api/butler/threads/{tid}/retry", handleButlerRetry)
		authed("POST", "/api/butler/confirms/{id}/apply", handleButlerConfirmApply)
		authed("POST", "/api/butler/confirms/{id}/discard", handleButlerConfirmDiscard)
	}
	return obs.Middleware(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// withProjectID bakes the route's {id} project into ctx so downstream
// obs.Info/Error calls inherit it. Runs after routing (PathValue is only
// set post-match) and after RequireAuth, inside the per-route wrapper —
// that is the "middleware level" for project injection. Empty id means a
// non-project route slipped in; it passes through untouched.
func withProjectID(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id := r.PathValue("id"); id != "" {
			r = r.WithContext(obs.WithProject(r.Context(), id))
		}
		next.ServeHTTP(w, r)
	}
}

// withSource bakes the origin subsystem into ctx (see obs.WithSource) so
// the tail's source facet is real. Applied to whole route blocks whose area
// is known statically — the preview block below. Everything else defaults
// to server.
func withSource(source string, h func(Deps) http.HandlerFunc) func(Deps) http.HandlerFunc {
	return func(d Deps) http.HandlerFunc {
		next := h(d)
		return func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(obs.WithSource(r.Context(), source)))
		}
	}
}

// obsFail logs a handler failure under the use-case key. err carries the
// cause for the deferred pattern: declare `var err error` early, defer a
// fail call guarded on err != nil, and assign err before every error
// return so the errors inbox sees every failure with its key.
func obsFail(r *http.Request, key, msg string, err error, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	data["error"] = err.Error()
	obs.Error(r.Context(), key, msg+": "+err.Error(), data)
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
// ?download=true forces a Content-Disposition attachment so browsers save
// the file instead of rendering it (used by the settings wipe flow).
func handleGetState(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(d.State.Path())
		if err != nil {
			writeInternalErr(w, "read state", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("download") == "true" {
			w.Header().Set("Content-Disposition", `attachment; filename="state.json"`)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	}
}

// handleDeleteState wipes every project, harness, and thread, resetting
// state.json to a bare document (user + server key only). The caller
// must have just downloaded state.json (handleGetState?download=true) —
// this is the destructive half of the settings reset flow.
//
// Each project is deleted via the existing project.Delete with ScopeAll,
// so containers, volumes, codemaps, and observe logs are all cleaned up
// using the same per-project pipeline. Non-builtin harnesses are stripped
// (builtins re-seed on the next boot via EnsureBuiltins).
func handleDeleteState(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ProjectDelete, "state wipe failed", err, nil)
			}
		}()
		// 1. Stop live surfaces and delete every project.
		entries, lerr := d.Projects.List()
		if lerr != nil {
			err = lerr
			writeInternalErr(w, "list projects for wipe", err)
			return
		}
		var firstErr error
		for _, e := range entries {
			if d.Preview != nil {
				_ = d.Preview.Stop(r.Context(), e.ID)
			}
			evictCDP(e.ID)
			if derr := d.Projects.Delete(r.Context(), e.ID, project.ScopeAll); derr != nil {
				slog.Warn("wipe: project delete failed", "id", e.ID, "err", derr)
				if firstErr == nil {
					firstErr = derr
				}
				continue
			}
			if d.Obs != nil {
				d.Obs.DeleteProject(e.ID)
			}
		}
		if firstErr != nil {
			err = firstErr
			writeInternalErr(w, "wipe projects", err)
			return
		}
		// 2. Strip all harnesses, then re-seed built-ins so the app stays
		// usable without a restart (builtins are the user's floor: Terminal,
		// OpenCode, etc. — EnsureBuiltins only adds missing ones).
		if d.Harnesses != nil {
			hs, herr := d.Harnesses.List()
			if herr != nil {
				err = herr
				writeInternalErr(w, "list harnesses for wipe", err)
				return
			}
			for _, h := range hs {
				if rerr := d.Harnesses.Remove(h.ID); rerr != nil {
					slog.Warn("wipe: harness remove failed", "id", h.ID, "err", rerr)
				}
			}
			if _, serr := d.Harnesses.EnsureBuiltins(); serr != nil {
				slog.Warn("wipe: reseed builtins failed", "err", serr)
			}
		}
		// 3. Reset state.json: clear projects, keep user + server key.
		// (Harnesses were already stripped above and re-seeded; the explicit
		// clear is a safety net for any custom harnesses that raced in.)
		err = d.State.Mutate(func(doc *state.Document) error {
			doc.Projects = map[string]state.Project{}
			return nil
		})
		if err != nil {
			writeInternalErr(w, "reset state", err)
			return
		}
		obs.Info(r.Context(), obs.ProjectDelete, "all data wiped", map[string]any{"projects": len(entries)})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "projectsDeleted": len(entries)})
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
