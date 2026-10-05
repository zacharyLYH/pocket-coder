// Desired-vs-live healthcheck: compares state.json against the system and
// reports every divergence in both directions, without changing anything.
// The Nerdy Stuff overview runs it on demand (button → synchronous POST →
// report on screen). No reconciliation lives here by design: the report
// tells the user exactly what is recorded-but-missing and what exists
// unrecorded, and the existing flows (install dialog, session launch,
// project recreate) remain the repair paths.
package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"

	"pcoder/internal/harness"
	"pcoder/internal/project"
	"pcoder/internal/state"
)

// healthStatus values for one check: ok (in sync), drift (state.json and
// the system disagree), unknown (the system side could not be probed,
// e.g. the container is not running).
const (
	healthOK      = "ok"
	healthDrift   = "drift"
	healthUnknown = "unknown"
)

// healthCheck is one compared fact: what state.json says (state) against
// what the system shows (system). Names read as paths: "container",
// "repo", "session:<name>", "harness:<id>".
type healthCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	State  string `json:"state"`
	System string `json:"system"`
	Detail string `json:"detail,omitempty"`
}

// handleProjectHealth runs the full desired-vs-live comparison for one
// project synchronously and returns the report. Read-only: it never
// creates, installs, or deletes anything.
func handleProjectHealth(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		p, st, err := d.Projects.Get(r.Context(), id)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "project not found")
				return
			}
			writeInternalErr(w, "read project", err)
			return
		}
		checks := checkProjectHealth(r.Context(), d, id, p, st)
		drift := false
		for _, c := range checks {
			if c.Status == healthDrift {
				drift = true
				break
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"project": id,
			"inSync":  !drift,
			"checks":  checks,
		})
	}
}

// checkProjectHealth builds the ordered report: container, repo, then
// sessions (recorded order irrelevant — sorted by name), then harnesses
// (registry order, already name-sorted). Exec-based checks degrade to
// unknown when the container is not running or a probe errors, never to a
// false ok or a false drift.
func checkProjectHealth(ctx context.Context, d Deps, id string, p project.Project, st project.Status) []healthCheck {
	container := project.ContainerName(id)
	checks := []healthCheck{}

	// Container: recorded by definition (we hold its state.json entry).
	c := healthCheck{Name: "container", State: "recorded"}
	switch st.State {
	case project.StateMissing:
		c.Status, c.System = healthDrift, "missing"
		c.Detail = "in state.json but the container is gone — recreate it from the project (container scope)"
	case project.StateRunning:
		c.Status, c.System = healthOK, "running"
	default:
		c.Status, c.System = healthOK, st.State
		c.Detail = "exists but not running — start it to probe inside"
	}
	checks = append(checks, c)
	if st.State != project.StateRunning {
		// Everything below needs exec inside a running container: report
		// unknown rather than guessing.
		repo := healthCheck{Name: "repo", Status: healthUnknown, State: p.Repo, System: "—", Detail: "container not running — start it to check the volume"}
		if p.Repo == "" {
			repo.Status, repo.State, repo.Detail = healthOK, "none recorded", ""
		}
		return append(checks,
			repo,
			healthCheck{Name: "sessions", Status: healthUnknown, State: "—", System: "—", Detail: "container not running — start it to compare tmux against recorded sessions"},
			healthCheck{Name: "harnesses", Status: healthUnknown, State: "—", System: "—", Detail: "container not running — start it to compare binaries against recorded installs"},
		)
	}

	checks = append(checks, repoCheck(ctx, d, container, p))

	// Sessions, both directions: recorded-but-gone and live-but-untracked.
	tmux, terr := d.Sessions.List(ctx, container)
	if terr != nil {
		for _, name := range sortedSessionNames(p) {
			checks = append(checks, healthCheck{Name: "session:" + name, Status: healthUnknown, State: describeSession(p.Sessions[name]), System: "—", Detail: "tmux list failed — cannot compare"})
		}
	} else {
		live := map[string]bool{}
		for _, e := range tmux {
			live[e.Name] = true
		}
		for _, name := range sortedSessionNames(p) {
			if live[name] {
				checks = append(checks, healthCheck{Name: "session:" + name, Status: healthOK, State: describeSession(p.Sessions[name]), System: "in tmux"})
			} else {
				checks = append(checks, healthCheck{Name: "session:" + name, Status: healthDrift, State: describeSession(p.Sessions[name]), System: "missing from tmux", Detail: "recorded but not running — re-enter it to relaunch, or delete it"})
			}
		}
		var ghosts []string
		for _, e := range tmux {
			if _, ok := p.Sessions[e.Name]; !ok {
				ghosts = append(ghosts, e.Name)
			}
		}
		sort.Strings(ghosts)
		for _, name := range ghosts {
			checks = append(checks, healthCheck{Name: "session:" + name, Status: healthDrift, State: "not recorded", System: "in tmux, untracked", Detail: "live session with no state.json entry — kill it from the terminal if it is stray"})
		}
	}

	// Harnesses, both directions: recorded-but-missing and present-but-unrecorded.
	registry, lerr := d.Harnesses.List()
	states, serr := d.Projects.HarnessStates(id)
	if lerr != nil || serr != nil {
		checks = append(checks, healthCheck{Name: "harnesses", Status: healthUnknown, State: "—", System: "—", Detail: "registry or record unreadable — cannot compare"})
		return checks
	}
	var cmds []string
	for _, h := range registry {
		if b := harness.Binary(h); b != "" {
			cmds = append(cmds, b)
		}
	}
	present, perr := d.Sessions.Installed(ctx, container, cmds)
	if perr != nil {
		for _, h := range registry {
			checks = append(checks, healthCheck{Name: "harness:" + h.ID, Status: healthUnknown, State: describeInstall(states[h.ID]), System: "—", Detail: "binary probe failed — cannot compare"})
		}
		return checks
	}
	for _, h := range registry {
		// The bash shell is a plain terminal, not a launch target: it is
		// always present and deliberately never recorded (same rule as the
		// UI's isLaunchable filter). Without this it would drift forever.
		if harness.Binary(h) == "bash" {
			continue
		}
		bin := harness.Binary(h)
		want := states[h.ID]
		got := bin != "" && present[bin]
		c := healthCheck{Name: "harness:" + h.ID, State: describeInstall(want), System: "missing"}
		if got {
			c.System = "binary present"
		}
		switch {
		case want == state.HarnessInstalled && got:
			c.Status = healthOK
		case want == state.HarnessInstalled:
			c.Status = healthDrift
			c.Detail = "recorded installed but the binary is missing — reinstall it from the harnesses dialog"
		case want == state.HarnessInstalling && got:
			c.Status = healthOK
			c.Detail = "install landed, record flip pending"
		case want == state.HarnessInstalling:
			c.Status = healthOK
			c.Detail = "install running on the server"
		case got:
			c.Status = healthDrift
			c.Detail = "binary present but not recorded — launch a session to heal the record, or install it explicitly"
		default:
			c.Status = healthOK
		}
		checks = append(checks, c)
	}
	return checks
}

// sortedSessionNames lists recorded session names, sorted for a stable report.
func sortedSessionNames(p project.Project) []string {
	names := make([]string, 0, len(p.Sessions))
	for name := range p.Sessions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// describeSession renders a recorded session for the state column.
func describeSession(s state.Session) string {
	if s.Harness == "" {
		return "recorded shell"
	}
	return "recorded harness " + s.Harness
}

// describeInstall renders a recorded install state for the state column.
// Absent and "false" both read as not installed ("false" is never written,
// absence means it).
func describeInstall(s state.HarnessStatus) string {
	switch s {
	case state.HarnessInstalled:
		return "installed"
	case state.HarnessInstalling:
		return "installing"
	default:
		return "not recorded"
	}
}

// repoCheck compares the recorded repo URL against the repo volume: empty
// means the clone never landed (or the volume is fresh) and boot's re-clone
// is the repair path.
func repoCheck(ctx context.Context, d Deps, container string, p project.Project) healthCheck {
	if p.Repo == "" {
		return healthCheck{Name: "repo", Status: healthOK, State: "none recorded", System: "—"}
	}
	// Through the session service (never d.Docker directly): its ExecCommand
	// returns trimmed output, and a non-zero exit surfaces as an error.
	out, err := d.Sessions.ExecCommand(ctx, container, "ls -A /workspace/repo")
	if err != nil {
		return healthCheck{Name: "repo", Status: healthUnknown, State: p.Repo, System: "—", Detail: "volume probe failed — cannot compare"}
	}
	if strings.TrimSpace(out) == "" {
		return healthCheck{Name: "repo", Status: healthDrift, State: p.Repo, System: "volume empty", Detail: "repo recorded but nothing cloned — recreate the container or re-clone from the terminal"}
	}
	return healthCheck{Name: "repo", Status: healthOK, State: p.Repo, System: "volume present"}
}
