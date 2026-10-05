// Butler read tools: names and counts only — no paths, no hunks, no
// secret values.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"pcoder/internal/agent"
	"pcoder/internal/docker"
	"pcoder/internal/harness"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/state"
)

// butlerStarted marks process start for the health tool's uptime.
var butlerStarted = time.Now()

// butlerJSON marshals v compactly for the model.
func butlerJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// butlerArgs decodes tool args into a string map. Missing args decode as empty.
func butlerArgs(argsJSON string) map[string]any {
	var m map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &m); err != nil {
		return map[string]any{}
	}
	return m
}

func butlerStr(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return strings.TrimSpace(s)
}

func butlerInt(args map[string]any, key string, def, max int) int {
	var n int
	switch v := args[key].(type) {
	case float64:
		n = int(v)
	case string:
		n, _ = strconv.Atoi(strings.TrimSpace(v))
	}
	if n <= 0 {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func butlerSchema(props map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": props, "additionalProperties": false}
}

var noProps = butlerSchema(map[string]any{})

func projectProp() map[string]any {
	return map[string]any{"project": map[string]any{"type": "string"}}
}

// butlerPreviewStatus reports one preview slot without ever exposing the
// token: stopped (no worker), degraded (worker unreachable), or ready
// (the browser answers CDP). Mirrors handlePreviewStatus minus the token.
func butlerPreviewStatus(ctx context.Context, d Deps, id string) string {
	if d.Preview == nil {
		return "stopped"
	}
	worker, err := d.Preview.Get(id)
	if err != nil {
		if errors.Is(err, preview.ErrNotFound) {
			return "stopped"
		}
		return "degraded"
	}
	s, err := getCDP(id, worker.Endpoint().CDP)
	if err != nil {
		return "degraded"
	}
	pctx, cancel := context.WithTimeout(ctx, statusPingTimeout)
	defer cancel()
	if _, err := s.call(pctx, "Runtime.evaluate", map[string]any{"expression": "1", "returnByValue": true}); err != nil {
		return "degraded"
	}
	return "ready"
}

// butlerReadTools returns the read tools bound to d. Every Run answers in
// redacted shape.
func butlerReadTools(d Deps) []agent.Tool {
	impls := map[string]agent.Tool{
		butlerToolListProjects: {
			Name:        butlerToolListProjects,
			Description: "List project ids, branches, and container status.",
			Schema:      noProps,
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				if d.Projects == nil {
					return "[]", nil
				}
				entries, err := d.Projects.List()
				if err != nil {
					return "", err
				}
				out := make([]map[string]any, 0, len(entries))
				for _, e := range entries {
					row := map[string]any{"id": e.ID}
					if p, st, gerr := d.Projects.Get(ctx, e.ID); gerr == nil {
						row["branch"] = p.Branch
						row["status"] = st.State
					} else {
						row["status"] = "unknown"
					}
					out = append(out, row)
				}
				return butlerJSON(out), nil
			},
		},
		butlerToolProjectDetail: {
			Name:        butlerToolProjectDetail,
			Description: "One project plus live container status. Args: {project}.",
			Schema:      butlerSchema(projectProp()),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				id := butlerStr(butlerArgs(argsJSON), "project")
				if id == "" {
					return "", fmt.Errorf("project is required")
				}
				if d.Projects == nil {
					return "", fmt.Errorf("no such project")
				}
				p, st, err := d.Projects.Get(ctx, id)
				if err != nil {
					return "", fmt.Errorf("no such project")
				}
				return butlerJSON(map[string]any{
					"id": id, "branch": p.Branch, "status": st.State,
					"harnesses": len(p.Harnesses), "sessions": len(p.Sessions),
					"preview": butlerPreviewStatus(ctx, d, id),
				}), nil
			},
		},
		butlerToolListSessions: {
			Name:        butlerToolListSessions,
			Description: "Tmux session names for one project. Args: {project}.",
			Schema:      butlerSchema(projectProp()),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				id := butlerStr(butlerArgs(argsJSON), "project")
				if id == "" {
					return "", fmt.Errorf("project is required")
				}
				if d.Sessions == nil {
					return "[]", nil
				}
				type sessRow struct {
					Name    string `json:"name"`
					Alive   bool   `json:"alive"`
					Harness string `json:"harness,omitempty"`
				}
				byName := map[string]*sessRow{}
				if live, err := d.Sessions.List(ctx, project.ContainerName(id)); err == nil {
					for _, e := range live {
						row := &sessRow{Name: e.Name}
						if alive, aerr := d.Sessions.IsAlive(ctx, project.ContainerName(id), e.Name); aerr == nil {
							row.Alive = alive
						}
						byName[e.Name] = row
					}
				}
				if d.State != nil { // merge recorded names (post-rebuild sessions) + harness metadata
					d.State.View(func(doc *state.Document) {
						if p, ok := doc.Projects[id]; ok {
							for n, meta := range p.Sessions {
								row, known := byName[n]
								if !known {
									row = &sessRow{Name: n}
									byName[n] = row
								}
								row.Harness = meta.Harness
							}
						}
					})
				}
				out := make([]sessRow, 0, len(byName))
				for _, row := range byName {
					out = append(out, *row)
				}
				sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
				return butlerJSON(out), nil
			},
		},
		butlerToolPreviewState: {
			Name:        butlerToolPreviewState,
			Description: "Preview slots (stopped/degraded/ready) and listening ports. Args: {project?} — omit for all projects.",
			Schema:      butlerSchema(map[string]any{"project": map[string]any{"type": "string"}}),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				ids := []string{butlerStr(butlerArgs(argsJSON), "project")}
				if ids[0] == "" {
					ids = nil
					if d.Projects != nil {
						if entries, err := d.Projects.List(); err == nil {
							for _, e := range entries {
								ids = append(ids, e.ID)
							}
						}
					}
				}
				out := make([]map[string]any, 0, len(ids))
				for _, id := range ids {
					row := map[string]any{"project": id, "status": butlerPreviewStatus(ctx, d, id), "ports": []int{}}
					if d.Sessions != nil && d.Projects != nil {
						if _, st, err := d.Projects.Get(ctx, id); err == nil && st.State == project.StateRunning {
							if raw, xerr := d.Sessions.ExecCommand(ctx, project.ContainerName(id),
								"ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true"); xerr == nil {
								ports := []int{}
								for _, s := range parseListeningPorts(raw) {
									switch p := s["port"].(type) {
									case int:
										ports = append(ports, p)
									case float64:
										ports = append(ports, int(p))
									}
								}
								row["ports"] = ports
							}
						}
					}
					out = append(out, row)
				}
				return butlerJSON(out), nil
			},
		},
		butlerToolGitMeta: {
			Name:        butlerToolGitMeta,
			Description: "Branch, changed-file count, upstream, ahead/behind, unborn, detached. No paths, no hunks. Args: {project}.",
			Schema:      butlerSchema(projectProp()),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				id := butlerStr(butlerArgs(argsJSON), "project")
				if id == "" {
					return "", fmt.Errorf("project is required")
				}
				if d.Projects == nil || d.Sessions == nil {
					return "", fmt.Errorf("no such project")
				}
				st, err := d.Projects.EnsureContainer(ctx, id)
				if err != nil {
					return "", fmt.Errorf("no such project")
				}
				if st.State != project.StateRunning {
					return butlerJSON(map[string]any{"project": id, "branch": "", "changedFiles": 0, "upstream": "",
						"ahead": 0, "behind": 0, "unborn": false, "detached": false, "running": false}), nil
				}
				container := project.ContainerName(id)
				dir, err := d.Sessions.RepoTarget(ctx, container)
				if err != nil {
					return "", err
				}
				qd := shellQuote(dir)
				branch, _ := d.Sessions.ExecCommand(ctx, container, "git -C "+qd+" rev-parse --abbrev-ref HEAD")
				branch = strings.TrimSpace(branch)
				// Sentinel first line: ExecCommand trims outer whitespace,
				// which would eat the leading XY space of an unstaged-only
				// first line like " M notes.txt" (same trick as git status).
				rawPorcelain, _ := d.Sessions.ExecCommand(ctx, container,
					"echo META-BEGIN; git -C "+qd+" status --porcelain=v1 --untracked-files=no; true")
				porcelain := rawPorcelain
				if i := strings.Index(rawPorcelain, "\n"); i >= 0 {
					porcelain = rawPorcelain[i+1:]
				}
				changed := 0
				for _, line := range strings.Split(porcelain, "\n") {
					if len(line) >= 4 {
						changed++
					}
				}
				ahead, behind := 0, 0
				upstream, _ := d.Sessions.ExecCommand(ctx, container,
					"git -C "+qd+" rev-parse --abbrev-ref @{u} 2>/dev/null || true")
				upstream = strings.TrimSpace(upstream)
				if upstream != "" {
					if counts, cerr := d.Sessions.ExecCommand(ctx, container,
						"git -C "+qd+" rev-list --left-right --count @{u}...HEAD 2>/dev/null || echo '0 0'"); cerr == nil {
						f := strings.Fields(counts)
						if len(f) == 2 {
							behind, _ = strconv.Atoi(f[0])
							ahead, _ = strconv.Atoi(f[1])
						}
					}
				}
				unborn := true
				if out, _ := d.Sessions.ExecCommand(ctx, container,
					"git -C "+qd+" rev-parse --verify --quiet HEAD >/dev/null 2>&1; echo $?"); strings.TrimSpace(out) == "0" {
					unborn = false
				}
				detached := branch == "" || branch == "HEAD"
				return butlerJSON(map[string]any{
					"project": id, "branch": branch, "upstream": upstream,
					"changedFiles": changed, "ahead": ahead, "behind": behind,
					"unborn": unborn, "detached": detached,
				}), nil
			},
		},
		butlerToolEventsTail: {
			Name:        butlerToolEventsTail,
			Description: "Recent event types with times. Args: {limit?, since?} — since is an event id.",
			Schema:      butlerSchema(map[string]any{"limit": map[string]any{"type": "number"}, "since": map[string]any{"type": "number"}}),
			Run: func(_ context.Context, argsJSON string) (string, error) {
				args := butlerArgs(argsJSON)
				limit := butlerInt(args, "limit", 20, 100)
				var since int64
				switch v := args["since"].(type) {
				case float64:
					since = int64(v)
				case string:
					since, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
				}
				if d.Events == nil {
					return "[]", nil
				}
				evs, err := d.Events.Read(since, limit)
				if err != nil {
					return "", err
				}
				out := make([]map[string]any, 0, len(evs))
				for _, e := range evs { // types + times only: data may hold secrets
					out = append(out, map[string]any{"id": e.ID, "type": e.Type, "time": e.Time.Format(time.RFC3339)})
				}
				return butlerJSON(out), nil
			},
		},
		butlerToolHealth: {
			Name:        butlerToolHealth,
			Description: "Disk free, uptime, docker ping, server version. Args: {project?} — with a project, adds its CPU/mem/disk numbers.",
			Schema:      butlerSchema(map[string]any{"project": map[string]any{"type": "string"}}),
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				dockerOK := false
				if d.Docker != nil {
					// Inspect on a missing name still proves the engine
					// answered: ErrNotFound means reachable.
					_, perr := d.Docker.Inspect(ctx, "butler-health-probe-never-exists")
					dockerOK = perr == nil || errors.Is(perr, docker.ErrNotFound)
				}
				var free uint64
				var fs syscall.Statfs_t
				if serr := syscall.Statfs(".", &fs); serr == nil {
					free = fs.Bavail * uint64(fs.Bsize)
				}
				out := map[string]any{
					"version": d.Version, "dockerPing": dockerOK,
					"uptimeSeconds": int64(time.Since(butlerStarted).Seconds()),
					"diskFreeBytes": free,
				}
				// Per-project resources: numbers only, same best-effort
				// blend as the observe stats tab.
				if id := butlerStr(butlerArgs(argsJSON), "project"); id != "" && d.Projects != nil && d.Docker != nil {
					if _, st, gerr := d.Projects.Get(ctx, id); gerr == nil && st.State == project.StateRunning {
						cid := project.ContainerName(id)
						if cs, serr := d.Docker.Stats(ctx, cid); serr == nil {
							out["cpuPercent"] = cs.CPUPercent
							out["memUsed"] = cs.MemUsed
							out["memLimit"] = cs.MemLimit
							out["pids"] = cs.PIDs
						}
						if used, total, derr := diskUsage(ctx, d.Docker, cid); derr == nil {
							out["diskUsed"] = used
							out["diskTotal"] = total
						}
					}
				}
				return butlerJSON(out), nil
			},
		},
		butlerToolHarnessInventory: {
			Name:        butlerToolHarnessInventory,
			Description: "Harnesses and the projects each is installed in (recorded installs). The built-in terminal shell is excluded. No args.",
			Schema:      noProps,
			Run: func(_ context.Context, _ string) (string, error) {
				if d.Harnesses == nil {
					return "[]", nil
				}
				all, err := d.Harnesses.List()
				if err != nil {
					return "", err
				}
				// Recorded per-project installs: harness id -> project ids.
				byHarness := map[string][]string{}
				if d.Projects != nil {
					if entries, perr := d.Projects.List(); perr == nil {
						for _, e := range entries {
							for _, hid := range e.Harnesses {
								byHarness[hid] = append(byHarness[hid], e.ID)
							}
						}
					}
				}
				out := make([]map[string]any, 0, len(all))
				for _, h := range all {
					if harness.Binary(h) == "bash" {
						continue // the terminal shell is not a launch target
					}
					projects := byHarness[h.ID]
					if projects == nil {
						projects = []string{}
					}
					out = append(out, map[string]any{"id": h.ID, "name": h.Name, "installedIn": projects})
				}
				return butlerJSON(out), nil
			},
		},
		butlerToolEnvNames: {
			Name:        butlerToolEnvNames,
			Description: "Known environment variable names. Names only, never values.",
			Schema:      noProps,
			Run: func(_ context.Context, argsJSON string) (string, error) {
				return butlerJSON([]string{
					"PCODER_LOGIN_EMAIL", "PCODER_DATA_DIR", "PCODER_BIND", "PCODER_DOCKER_SOCK",
					"SMTP_HOST", "SMTP_PORT", "SMTP_USER", "SMTP_PASSWORD", "SMTP_FROM",
				}), nil
			},
		},
		butlerToolConfigStatus: {
			Name:        butlerToolConfigStatus,
			Description: "Setup booleans: ai, server deploy key, smtp. Booleans only.",
			Schema:      noProps,
			Run: func(_ context.Context, argsJSON string) (string, error) {
				var ssh, smtp bool
				if d.SSHKeys != nil {
					_, ssh = d.SSHKeys.Get()
				}
				if d.State != nil {
					d.State.View(func(doc *state.Document) {
						smtp = doc.SMTP != nil
					})
				}
				return butlerJSON(map[string]any{
					"aiConfigured": aiConfig(d, aiBody{}).Valid(),
					"sshKeys":      ssh,
					"smtp":         smtp,
				}), nil
			},
		},
		butlerToolListAIModels: {
			Name:        butlerToolListAIModels,
			Description: "Configured AI model entries: id, label, model name, and whether a key is stored. Names only, never key values. No args.",
			Schema:      noProps,
			Run: func(_ context.Context, _ string) (string, error) {
				if d.State == nil {
					return "[]", nil
				}
				out := []map[string]any{}
				d.State.View(func(doc *state.Document) {
					for _, m := range doc.AIModels {
						out = append(out, aiListItem(m)) // id/label/model/hasKey; never the key
					}
				})
				return butlerJSON(out), nil
			},
		},
		butlerToolArchitecture: {
			Name:        butlerToolArchitecture,
			Description: "Design notes: how the system is built and why. Read when a question needs design context (what survives deletes, where state lives). Args: {section?} — overview, state, projects, sessions, previews, harnesses, butler; omit for all.",
			Schema:      butlerSchema(map[string]any{"section": map[string]any{"type": "string"}}),
			Run: func(_ context.Context, argsJSON string) (string, error) {
				sections := butlerArchSections()
				want := strings.ToLower(butlerStr(butlerArgs(argsJSON), "section"))
				if want == "" {
					return butlerArchDoc, nil
				}
				if doc, ok := sections[want]; ok {
					return doc, nil
				}
				return "Unknown section. Known: " + strings.Join(butlerArchSectionNames(), ", ") + ".", nil
			},
		},
	}
	out := make([]agent.Tool, 0, len(butlerReadNames))
	for _, name := range butlerReadNames {
		tl, ok := impls[name]
		if !ok {
			continue // TestButlerToolNames pins names against impls
		}
		out = append(out, tl)
	}
	return out
}
