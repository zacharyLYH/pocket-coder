// Project management endpoints: create, list, get, delete, and lifecycle
// operations (start/stop/restart).
package httpapi

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	"sps/internal/docker"
	"sps/internal/project"
	"sps/internal/state"
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
				"quickCommands": p.QuickCommands,
			})
		}
	}
}

func handlePatchProject(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.State == nil {
			writeErr(w, http.StatusInternalServerError, "state not available")
			return
		}
		var body struct {
			QuickCommands *map[string]string `json:"quickCommands"`
			Name          *string            `json:"name"`
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
					writeErr(w, http.StatusBadRequest, "alias and command must be non-empty")
					return
				}
				if !isValidAlias(alias) {
					writeErr(w, http.StatusBadRequest, "invalid alias: "+alias)
					return
				}
			}
		}
		err := d.State.Mutate(func(doc *state.Document) error {
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
			if body.Name != nil {
				name := strings.TrimSpace(*body.Name)
				if name == "" {
					return errors.New("name must be non-empty")
				}
				p.Name = name
			}
			doc.Projects[id] = p
			return nil
		})
		if errors.Is(err, project.ErrNotFound) {
			writeErr(w, http.StatusNotFound, "no such project")
			return
		}
		if err != nil {
			if strings.Contains(err.Error(), "name must") {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			writeInternalErr(w, "patch project", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

var aliasRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func isValidAlias(s string) bool { return aliasRe.MatchString(s) }

// handleProjectOp serves POST /{id}/start|stop|restart.
func handleProjectOp(d Deps, op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		ctx := r.Context()
		if d.Preview != nil && (op == "stop" || op == "restart") {
			_ = d.Preview.Stop(ctx, id)
		}
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
		if d.Preview != nil {
			_ = d.Preview.Stop(r.Context(), r.PathValue("id"))
		}
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
