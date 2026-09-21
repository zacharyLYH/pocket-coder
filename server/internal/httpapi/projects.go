// Project management endpoints: create, list, get, delete, and lifecycle
// operations (start/stop/restart).
package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"pcoder/internal/docker"
	"pcoder/internal/obs"
	"pcoder/internal/project"
	"pcoder/internal/state"
)

// handleCreateProject runs the create pipeline synchronously: project up,
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
		if !gitConfigured(d) {
			writeErr(w, http.StatusConflict, "git not configured")
			return
		}
		// No middleware injection here: the id doesn't exist until Create
		// parses the repo URL, so there is no project ctx to log under on
		// pre-parse failures. The service injects once known and owns all
		// create logging (project.create/ready on success, project.clone
		// on clone failure; same trace: the request ctx flows into Create).
		id, p, err := d.Projects.Create(r.Context(), strings.TrimSpace(body.RepoURL), strings.TrimSpace(body.Branch), strings.TrimSpace(body.CloneMethod))
		switch {
		case errors.Is(err, project.ErrInvalidInput):
			writeErr(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, project.ErrConflict):
			writeErr(w, http.StatusConflict, err.Error())
		case err != nil && isGitAuthError(err.Error()):
			writeErr(w, http.StatusBadGateway, gitAuthMsg)
		case err != nil:
			writeErr(w, http.StatusInternalServerError, err.Error())
		default:
			writeJSON(w, http.StatusCreated, map[string]any{
				"id": id, "repo": p.Repo, "branch": p.Branch,
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
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ProjectGet, "get project failed", err, nil)
			}
		}()
		p, status, gerr := d.Projects.Get(r.Context(), r.PathValue("id"))
		if gerr != nil {
			err = gerr
			if errors.Is(gerr, project.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "no such project")
			} else {
				writeInternalErr(w, "get project", gerr)
			}
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"id": r.PathValue("id"), "repo": p.Repo,
			"branch": p.Branch, "cloneMethod": p.CloneMethod, "status": status.State,
			"quickCommands": p.QuickCommands,
		})
	}
}

func handlePatchProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ProjectPatch, "patch project failed", err, nil)
			}
		}()
		if d.State == nil {
			err = errors.New("state not available")
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		var body struct {
			QuickCommands *map[string]string `json:"quickCommands"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		id := r.PathValue("id")
		if body.QuickCommands != nil {
			for alias, cmd := range *body.QuickCommands {
				alias = strings.TrimSpace(alias)
				cmd = strings.TrimSpace(cmd)
				if alias == "" || cmd == "" {
					err = errors.New("alias and command must be non-empty")
					writeErr(w, http.StatusBadRequest, err.Error())
					return
				}
				if !isValidAlias(alias) {
					err = errors.New("invalid alias: " + alias)
					writeErr(w, http.StatusBadRequest, err.Error())
					return
				}
			}
		}
		err = d.State.Mutate(func(doc *state.Document) error {
			p, ok := doc.Projects[id]
			if !ok {
				return project.ErrNotFound
			}
			if body.QuickCommands != nil {
				// normalize: trim and validate already done
				normalized := map[string]string{}
				for k, v := range *body.QuickCommands {
					normalized[strings.TrimSpace(k)] = strings.TrimSpace(v)
				}
				p.QuickCommands = normalized
				if len(normalized) == 0 {
					p.QuickCommands = nil
				}
			}
			doc.Projects[id] = p
			return nil
		})
		if errors.Is(err, project.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such project")
			return
		}
		if err != nil {
			writeInternalErr(w, "patch project", err)
			return
		}
		obs.Info(r.Context(), obs.ProjectPatch, "project patched", nil)
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

var aliasRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func isValidAlias(s string) bool { return aliasRe.MatchString(s) }

// handleProjectOp serves POST /{id}/start|stop|restart.
func handleProjectOp(d Deps, op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := map[string]string{
			"start":   obs.ProjectStart,
			"stop":    obs.ProjectStop,
			"restart": obs.ProjectRestart,
		}[op]
		var err error
		defer func() {
			if err != nil {
				obsFail(r, key, "project "+op+" failed", err, map[string]any{"op": op})
			}
		}()
		id := r.PathValue("id")
		ctx := r.Context()
		if d.Preview != nil && (op == "stop" || op == "restart") {
			_ = d.Preview.Stop(ctx, id)
			// Drop the CDP session too: it points at the stopped worker's
			// websocket, and the next tools call would dial into the void
			// until its 30s timeout instead of failing fast.
			evictCDP(id)
		}
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
			// The start/stop Info lines come from the service; restart is
			// stop+start (two lines). One audit line keeps the op itself
			// visible as a unit.
			obs.Info(ctx, key, "project "+op+" ok", map[string]any{"op": op})
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		}
	}
}

func handleDeleteProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.ProjectDelete, "delete project failed", err, nil)
			}
		}()
		if d.Preview != nil {
			_ = d.Preview.Stop(r.Context(), r.PathValue("id"))
		}
		evictCDP(r.PathValue("id"))
		scope := project.Scope(r.URL.Query().Get("scope"))
		if scope == "" {
			scope = project.ScopeAll
		}
		err = d.Projects.Delete(r.Context(), r.PathValue("id"), scope)
		switch {
		case err == nil:
			if d.Obs != nil {
				d.Obs.DeleteProject(r.PathValue("id"))
			}
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
