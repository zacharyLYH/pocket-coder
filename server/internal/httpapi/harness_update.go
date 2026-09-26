// Harness update checks: the harnesses tab probes whether an installed CLI
// is behind its registry version, and the Update button re-runs the
// harness's own install command (npm always fetches latest, so install IS
// the update — no separate machinery).
package httpapi

import (
	"net/http"
	"regexp"
	"strings"

	"pcoder/internal/harness"
	"pcoder/internal/project"
)

var (
	npmInstallRe = regexp.MustCompile(`npm\s+(?:install|i)\s+(?:[^&|;]*?\s)?(?:-g|--global)(?:\s+--[^\s]+)*\s+([^\s&|;]+)`)
	versionRe    = regexp.MustCompile(`v?(\d+\.\d+\.\d+[^,\s]*)`)
)

// npmPackage extracts the registry package from an install command like
// "npm i -g opencode-ai". Empty when the install is not an npm global.
func npmPackage(install string) string {
	m := npmInstallRe.FindStringSubmatch(install)
	if m == nil {
		return ""
	}
	return m[1]
}

// normVersion pulls the first version-like token out of noisy
// `--version` output ("claude 1.2.3" → "1.2.3"). Empty when none found.
func normVersion(out string) string {
	m := versionRe.FindStringSubmatch(out)
	if m == nil {
		return strings.TrimSpace(out)
	}
	return m[1]
}

type updateCheckBody struct {
	ProjectIDs []string `json:"projectIds"`
}

// handleHarnessUpdateCheck compares the installed CLI version against the
// registry in one running container. Non-npm installs report unavailable;
// with no running container among the candidates it answers 409.
func handleHarnessUpdateCheck(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		h, err := d.Harnesses.Get(id)
		if err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		pkg := npmPackage(h.Install)
		if pkg == "" {
			writeJSON(w, http.StatusOK, map[string]any{
				"unavailable": "no npm package detected — Update re-runs the install command",
			})
			return
		}
		var body updateCheckBody
		if !decodeBody(w, r, &body, true) {
			return
		}
		ids := body.ProjectIDs
		if len(ids) == 0 {
			entries, lerr := d.Projects.List()
			if lerr != nil {
				writeInternalErr(w, "list projects", lerr)
				return
			}
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
		}
		container := ""
		probed := ""
		for _, pid := range ids {
			st, cerr := d.Projects.EnsureContainer(r.Context(), pid)
			if cerr != nil || st.State != project.StateRunning {
				continue
			}
			container = project.ContainerName(pid)
			probed = pid
			break
		}
		if container == "" {
			writeErr(w, http.StatusConflict, "start a project first — the check runs inside a running container")
			return
		}
		current := ""
		if out, verr := d.Sessions.ExecCommand(r.Context(), container, harness.Binary(h)+" --version"); verr == nil {
			current = normVersion(out)
		}
		latest, lerr := d.Sessions.ExecCommand(r.Context(), container, "npm view "+pkg+" version")
		if lerr != nil {
			writeErr(w, http.StatusBadGateway, "registry lookup failed: "+lerr.Error())
			return
		}
		latest = normVersion(latest)
		_, _ = d.Events.Append("harness.update_check", map[string]any{
			"id": id, "project": probed, "current": current, "latest": latest,
		})
		writeJSON(w, http.StatusOK, map[string]any{
			"project": probed, "current": current, "latest": latest,
			"updateAvailable": current != "" && current != latest,
		})
	}
}
