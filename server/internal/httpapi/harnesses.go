// Harness registry endpoints: the "+ New Session" list, the add-harness
// form, and the explicit home-page install. The form and the folder are the
// same thing — saving writes a plugin file (PRD §5).
package httpapi

import (
	"fmt"
	"net/http"

	"pcoder/internal/harness"
	"pcoder/internal/project"
)

func handleListHarnesses(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		harnesses, err := d.Harnesses.List()
		if err != nil {
			writeInternalErr(w, "list harnesses", err)
			return
		}
		if harnesses == nil {
			harnesses = []harness.Harness{}
		}
		writeJSON(w, http.StatusOK, map[string]any{"harnesses": harnesses})
	}
}

func handleCreateHarness(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name    string `json:"name"`
			Command string `json:"command"`
			Install string `json:"install"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		id, err := d.Harnesses.Save(harness.Harness{
			Name:    body.Name,
			Command: body.Command,
			Install: body.Install,
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		_, _ = d.Events.Append("harness.added", map[string]any{"id": id})
		writeJSON(w, http.StatusCreated, map[string]any{"id": id, "name": body.Name, "command": body.Command})
	}
}

// handleInstallHarness runs a harness's install synchronously into the
// selected projects — the explicit, home-page "inject it now" flow. The
// download happens here, not lazily at session launch, so errors surface
// while the user is looking at them. Per-project results are returned;
// stopped containers are skipped (launch-time install-on-demand remains the
// fallback for anything skipped). Projects created later are never
// auto-injected: the user decides, from this same page.
func handleInstallHarness(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		h, err := d.Harnesses.Get(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		if h.Install == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": fmt.Sprintf("%q has no install command — nothing to download", h.Name),
			})
			return
		}
		var body struct {
			ProjectIDs []string `json:"projectIds"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if len(body.ProjectIDs) == 0 {
			writeErr(w, http.StatusBadRequest, "select at least one project")
			return
		}
		results := runInProjects(d, r, body.ProjectIDs, func(container string) (string, error) {
			return "", d.Sessions.InstallHarness(r.Context(), container, h)
		})
		for i, res := range results {
			if i < len(body.ProjectIDs) && res.Status == "ok" {
				_ = d.Projects.RecordInstall(body.ProjectIDs[i], id)
			}
		}
		_, _ = d.Events.Append("harness.install", map[string]any{"id": id, "results": results})
		writeJSON(w, http.StatusOK, map[string]any{"results": results})
	}
}

// handleDeleteHarness removes a harness from the registry. Deleting an
// unknown harness is idempotent success. Builtins deleted this way come
// back on the next boot (EnsureBuiltins re-seeds missing entries).
func handleDeleteHarness(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := d.Harnesses.Remove(id); err != nil {
			writeInternalErr(w, "delete harness", err)
			return
		}
		_, _ = d.Events.Append("harness.deleted", map[string]any{"id": id})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// handleProjectHarnesses lists the harness registry annotated with what is
// actually installed in THIS project's container, so "+ New Session" can
// show what is ready and what will self-heal on launch.
func handleProjectHarnesses(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := ensureProject(d, w, r)
		if !ok {
			return
		}
		harnesses, err := d.Harnesses.List()
		if err != nil {
			writeInternalErr(w, "list harnesses", err)
			return
		}
		cmds := make([]string, len(harnesses))
		for i, h := range harnesses {
			cmds[i] = harness.Binary(h)
		}
		installed, err := d.Sessions.Installed(r.Context(), project.ContainerName(id), cmds)
		if err != nil {
			writeInternalErr(w, "probe harnesses", err)
			return
		}
		type entry struct {
			ID        string `json:"id"`
			Name      string `json:"name"`
			Command   string `json:"command"`
			Install   string `json:"install,omitempty"`
			Installed bool   `json:"installed"`
		}
		out := make([]entry, 0, len(harnesses))
		for _, h := range harnesses {
			out = append(out, entry{ID: h.ID, Name: h.Name, Command: h.Command, Install: h.Install, Installed: installed[harness.Binary(h)]})
		}
		writeJSON(w, http.StatusOK, map[string]any{"harnesses": out})
	}
}
