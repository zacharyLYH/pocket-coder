// Butler write tools: propose-only.
//
// A write tool call never mutates. It validates args, computes the blast
// radius from live state, stores the real work via butlerPropose, and
// returns {needsConfirm, confirmId, summary, blastRadius}. Confirm/apply
// runs the stored work; discard drops it.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"pcoder/internal/agent"
	"pcoder/internal/butlerthreads"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
)

// butlerWriteDef is one write tool: blast is pure (no mutation), exec
// does the work after Confirm. secret carries the masked apply value
// (env fix only); every other tool ignores it.
type butlerWriteDef struct {
	name   string
	desc   string
	schema map[string]any
	blast  func(d Deps, ctx context.Context, args map[string]any) (summary, blast string, err error)
	exec   func(ctx context.Context, d Deps, args map[string]any, secret string) (string, error)
}

func strProp() map[string]any { return map[string]any{"type": "string"} }

// butlerContainer resolves a project's container + repo dir, requiring a
// running container. Shared by every container-scoped write.
func butlerContainer(ctx context.Context, d Deps, id string) (container, dir string, err error) {
	if id == "" {
		return "", "", fmt.Errorf("project is required")
	}
	if d.Projects == nil || d.Sessions == nil {
		return "", "", fmt.Errorf("no such project")
	}
	st, serr := d.Projects.EnsureContainer(ctx, id)
	if serr != nil {
		return "", "", fmt.Errorf("no such project")
	}
	if st.State != project.StateRunning {
		return "", "", fmt.Errorf("container not running")
	}
	container = project.ContainerName(id)
	dir, derr := d.Sessions.RepoTarget(ctx, container)
	if derr != nil {
		return "", "", derr
	}
	return container, dir, nil
}

// butlerSessionCount counts sessions for blast-radius lines. Best-effort:
// unknown reads as 0 rather than failing the proposal.
func butlerSessionCount(ctx context.Context, d Deps, id string) int {
	if d.Sessions == nil {
		return 0
	}
	live, err := d.Sessions.List(ctx, project.ContainerName(id))
	if err != nil {
		return 0
	}
	return len(live)
}

// butlerLiveEnums reads current ids out of state so tool schemas can
// constrain them at request-build time — the codex pattern: determinism
// comes from the harness constraining args, not from the model remembering.
// Each map is schema-prop-name -> id list; tools not listed stay open.
func butlerLiveEnums(d Deps) map[string]map[string][]string {
	enums := map[string]map[string][]string{}
	add := func(tool, prop string, ids []string) {
		if len(ids) == 0 {
			return
		}
		if enums[tool] == nil {
			enums[tool] = map[string][]string{}
		}
		enums[tool][prop] = ids
	}
	if d.State != nil {
		d.State.View(func(doc *state.Document) {
			hids := make([]string, 0, len(doc.Harnesses))
			for id := range doc.Harnesses {
				hids = append(hids, id)
			}
			sort.Strings(hids)
			add(butlerToolSwitchModel, "harness", hids)
			add(butlerToolDeleteHarness, "id", hids)
			mids := make([]string, 0, len(doc.AIModels))
			for _, m := range doc.AIModels {
				mids = append(mids, m.ID)
			}
			add(butlerToolUpdateAIModel, "id", mids)
			gids := make([]string, 0, len(doc.GitIDs))
			for _, g := range doc.GitIDs {
				gids = append(gids, g.ID)
			}
			add(butlerToolSaveGitIdentity, "identityId", gids)
		})
	}
	if d.Projects != nil {
		if entries, err := d.Projects.List(); err == nil {
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.ID)
			}
			for _, tool := range []string{butlerToolStart, butlerToolStop, butlerToolRestart,
				butlerToolDeleteProject, butlerToolSessionCreate, butlerToolSessionKill,
				butlerToolSessionRestart, butlerToolSessionRename, butlerToolPreviewStart,
				butlerToolPreviewClose, butlerToolGitPull, butlerToolGitPush, butlerToolGitSwitch,
				butlerToolSaveShortcut, butlerToolSaveGitIdentity, butlerToolInstallHarness,
				butlerToolFanoutExec} {
				add(tool, "project", ids)
			}
		}
	}
	return enums
}

// butlerConstrainSchema deep-copies the def schema and pins enum values
// onto the named props. Unknown props are ignored. Best effort: when a
// list is empty (no state yet) the arg stays open and validation
// remains the backstop.
func butlerConstrainSchema(schema map[string]any, props map[string][]string) map[string]any {
	raw, err := json.Marshal(schema)
	if err != nil {
		return schema
	}
	var clone map[string]any
	if json.Unmarshal(raw, &clone) != nil {
		return schema
	}
	properties, _ := clone["properties"].(map[string]any)
	for name, values := range props {
		p, ok := properties[name].(map[string]any)
		if !ok {
			continue
		}
		p["enum"] = values
	}
	return clone
}

// butlerWriteTools wraps the table: wall check, blast, propose. Schemas
// are constrained with live-state ids before shipping to the model.
func butlerWriteTools(d Deps, st *butlerthreads.Store, threadID, turnID string, created *[]butlerCard) []agent.Tool {
	byName := make(map[string]butlerWriteDef, len(butlerWriteTable))
	for _, def := range butlerWriteTable {
		byName[def.name] = def
	}
	enums := butlerLiveEnums(d)
	out := make([]agent.Tool, 0, len(butlerWriteNames))
	for _, name := range butlerWriteNames {
		def, ok := byName[name]
		if !ok {
			continue // TestButlerToolNames pins names against the table
		}
		schema := def.schema
		if props, ok := enums[name]; ok {
			schema = butlerConstrainSchema(schema, props)
		}
		out = append(out, agent.Tool{
			Name:        def.name,
			Description: def.desc + " Needs Confirm: proposing returns a confirm card; nothing runs until Confirm.",
			Schema:      schema,
			Run: func(ctx context.Context, argsJSON string) (string, error) {
				args := butlerArgs(argsJSON)
				summary, blast, err := def.blast(d, ctx, args)
				if err != nil {
					return "", err
				}
				id, err := butlerPropose(st, threadID, turnID, def.name, argsJSON, summary, blast, created)
				if err != nil {
					return "", err
				}
				return butlerJSON(map[string]any{
					"needsConfirm": true, "confirmId": id,
					"summary": summary, "blastRadius": blast,
				}), nil
			},
		})
	}
	return out
}

// butlerWriteTable is every write tool.
var butlerWriteTable = []butlerWriteDef{
	{
		name: butlerToolCreateProject, desc: "Clone a repo URL and branch, then report Ready.",
		schema: butlerSchema(map[string]any{"repoUrl": strProp(), "branch": strProp(), "cloneMethod": strProp()}),
		blast: func(d Deps, _ context.Context, args map[string]any) (string, string, error) {
			url := butlerStr(args, "repoUrl")
			if url == "" {
				return "", "", fmt.Errorf("repoUrl is required")
			}
			branch := butlerStr(args, "branch")
			return fmt.Sprintf("Clone %s on %s?", url, orDefault(branch, "default branch")),
				"Creates one project container and clones the repo, then reports Ready.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Projects == nil || !gitConfigured(d) {
				return "", fmt.Errorf("git not configured")
			}
			id, _, err := d.Projects.Create(ctx, butlerStr(args, "repoUrl"), butlerStr(args, "branch"), butlerStr(args, "cloneMethod"))
			if err != nil {
				return "", err
			}
			return "Ready: " + id, nil
		},
	},
	{
		name: butlerToolStart, desc: "Start one project container.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Start project " + id + "?", "Starts its container. No data is touched.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Projects == nil {
				return "", fmt.Errorf("no such project")
			}
			return "Started.", d.Projects.Start(ctx, butlerStr(args, "project"))
		},
	},
	{
		name: butlerToolStop, desc: "Stop one project container.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Stop project " + id + "?", "Stops its container and preview. Sessions end.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Projects == nil {
				return "", fmt.Errorf("no such project")
			}
			id := butlerStr(args, "project")
			if d.Preview != nil {
				_ = d.Preview.Stop(ctx, id)
			}
			evictCDP(id)
			return "Stopped.", d.Projects.Stop(ctx, id)
		},
	},
	{
		name: butlerToolRestart, desc: "Restart one project container.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Restart project " + id + "?", "Stops then starts its container and preview.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Projects == nil {
				return "", fmt.Errorf("no such project")
			}
			id := butlerStr(args, "project")
			if d.Preview != nil {
				_ = d.Preview.Stop(ctx, id)
			}
			evictCDP(id)
			return "Restarted.", d.Projects.Restart(ctx, id)
		},
	},
	{
		name: butlerToolSessionCreate, desc: "Create a plain-shell tmux session.",
		schema: butlerSchema(map[string]any{"project": strProp(), "name": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if id == "" || !session.ValidName(name) {
				return "", "", fmt.Errorf("project and a valid session name are required")
			}
			return fmt.Sprintf("Create session %s in %s?", name, id), "Adds one tmux session.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if _, _, err := butlerContainer(ctx, d, id); err != nil {
				return "", err
			}
			if err := d.Sessions.Create(ctx, project.ContainerName(id), name); err != nil {
				return "", err
			}
			_ = d.Projects.RecordSession(id, name, "")
			return "Created " + name, nil
		},
	},
	{
		name: butlerToolSessionKill, desc: "Kill a tmux session.",
		schema: butlerSchema(map[string]any{"project": strProp(), "name": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if id == "" || !session.ValidName(name) {
				return "", "", fmt.Errorf("project and a valid session name are required")
			}
			return fmt.Sprintf("Kill session %s in %s?", name, id), "Ends that session. Other sessions keep running.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if _, _, err := butlerContainer(ctx, d, id); err != nil {
				return "", err
			}
			if err := d.Sessions.Kill(ctx, project.ContainerName(id), name); err != nil {
				return "", err
			}
			_ = d.Projects.RemoveSession(id, name)
			return "Killed " + name, nil
		},
	},
	{
		name: butlerToolSessionRestart, desc: "Restart a tmux session (kill + relaunch).",
		schema: butlerSchema(map[string]any{"project": strProp(), "name": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if id == "" || !session.ValidName(name) {
				return "", "", fmt.Errorf("project and a valid session name are required")
			}
			return fmt.Sprintf("Restart session %s in %s?", name, id), "Kills it and relaunches the same harness or shell.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			id, name := butlerStr(args, "project"), butlerStr(args, "name")
			if _, _, err := butlerContainer(ctx, d, id); err != nil {
				return "", err
			}
			container := project.ContainerName(id)
			_ = d.Sessions.Kill(ctx, container, name)
			if sess, ok := d.Projects.GetSession(id, name); ok && sess.Harness != "" {
				if h, herr := d.Harnesses.Get(sess.Harness); herr == nil {
					if _, lerr := d.Sessions.LaunchNamed(ctx, container, name, h); lerr != nil {
						return "", lerr
					}
					return "Restarted " + name, nil
				}
			}
			if err := d.Sessions.Create(ctx, container, name); err != nil {
				return "", err
			}
			return "Restarted " + name, nil
		},
	},
	{
		name: butlerToolSessionRename, desc: "Rename a tmux session.",
		schema: butlerSchema(map[string]any{"project": strProp(), "old": strProp(), "new": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, old, new := butlerStr(args, "project"), butlerStr(args, "old"), butlerStr(args, "new")
			if id == "" || !session.ValidName(old) || !session.ValidName(new) {
				return "", "", fmt.Errorf("project, old, and a valid new name are required")
			}
			return fmt.Sprintf("Rename session %s to %s in %s?", old, new, id), "Renames one session. Live attaches survive.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			id := butlerStr(args, "project")
			if _, _, err := butlerContainer(ctx, d, id); err != nil {
				return "", err
			}
			return "Renamed.", d.Sessions.Rename(ctx, project.ContainerName(id), butlerStr(args, "old"), butlerStr(args, "new"))
		},
	},
	{
		name: butlerToolPreviewStart, desc: "Open a preview port.",
		schema: butlerSchema(map[string]any{"project": strProp(), "port": map[string]any{"type": "number"}}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			port, _ := args["port"].(float64)
			if id == "" || port <= 0 || port > 65535 {
				return "", "", fmt.Errorf("project and a port 1-65535 are required")
			}
			return fmt.Sprintf("Preview %s on :%d?", id, int(port)), "Opens the preview sidecar against that port.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Preview == nil {
				return "", fmt.Errorf("no previews")
			}
			id := butlerStr(args, "project")
			if _, _, err := butlerContainer(ctx, d, id); err != nil {
				return "", err
			}
			_, err := d.Preview.Ensure(ctx, preview.Config{
				ProjectID: id, ContainerID: project.ContainerName(id), Port: butlerInt(args, "port", 0, 65535),
			})
			if err != nil {
				return "", err
			}
			return "Preview starting.", nil
		},
	},
	{
		name: butlerToolPreviewClose, desc: "Close the preview.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Close preview for " + id + "?", "Stops the preview sidecar. The app server keeps running.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Preview == nil {
				return "", fmt.Errorf("no previews")
			}
			evictCDP(butlerStr(args, "project"))
			return "Preview closed.", d.Preview.Stop(ctx, butlerStr(args, "project"))
		},
	},
	{
		name: butlerToolGitPull, desc: "Pull fast-forward only.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Pull " + id + "?", "Runs git pull --ff-only. Never force-pushes.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			container, dir, err := butlerContainer(ctx, d, butlerStr(args, "project"))
			if err != nil {
				return "", err
			}
			out, xerr := d.Sessions.ExecCommand(ctx, container, "GIT_TERMINAL_PROMPT=0 git -C "+shellQuote(dir)+" pull --ff-only")
			if xerr != nil {
				return "", fmt.Errorf("%s", strings.TrimSpace(out))
			}
			return tailLines(strings.TrimSpace(out), 10), nil
		},
	},
	{
		name: butlerToolGitPush, desc: "Push the current branch upstream.",
		schema: butlerSchema(projectProp()),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			return "Push " + id + "?", "Pushes the current branch with -u origin. No force.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			container, dir, err := butlerContainer(ctx, d, butlerStr(args, "project"))
			if err != nil {
				return "", err
			}
			branch, _ := d.Sessions.ExecCommand(ctx, container, "git -C "+shellQuote(dir)+" rev-parse --abbrev-ref HEAD")
			branch = strings.TrimSpace(branch)
			if branch == "" || branch == "HEAD" {
				return "", fmt.Errorf("detached HEAD — switch to a branch before pushing")
			}
			out, xerr := d.Sessions.ExecCommand(ctx, container,
				"GIT_TERMINAL_PROMPT=0 git -C "+shellQuote(dir)+" push -u origin "+shellQuote(branch))
			if xerr != nil {
				return "", fmt.Errorf("%s", strings.TrimSpace(out))
			}
			return tailLines(strings.TrimSpace(out), 10), nil
		},
	},
	{
		name: butlerToolGitSwitch, desc: "Switch branches (boring git ops only).",
		schema: butlerSchema(map[string]any{"project": strProp(), "branch": strProp(), "create": map[string]any{"type": "boolean"}}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, branch := butlerStr(args, "project"), butlerStr(args, "branch")
			if id == "" || branch == "" {
				return "", "", fmt.Errorf("project and branch are required")
			}
			return fmt.Sprintf("Switch %s to %s?", id, branch), "Refuses when the tree is dirty or conflicted.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			container, dir, err := butlerContainer(ctx, d, butlerStr(args, "project"))
			if err != nil {
				return "", err
			}
			name := butlerStr(args, "branch")
			flag := ""
			if yes, _ := args["create"].(bool); yes {
				flag = "-c "
			}
			out, xerr := d.Sessions.ExecCommand(ctx, container, "git -C "+shellQuote(dir)+" switch "+flag+shellQuote(name))
			if xerr != nil {
				return "", fmt.Errorf("%s", strings.TrimSpace(out))
			}
			return "On " + name, nil
		},
	},

	// ── sensitive writes ──
	{
		name: butlerToolDeleteProject, desc: "Remove a container, repo, or metadata by scope.",
		schema: butlerSchema(map[string]any{"project": strProp(), "scope": strProp()}),
		blast: func(d Deps, ctx context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "project")
			if id == "" {
				return "", "", fmt.Errorf("project is required")
			}
			scope := project.Scope(orDefault(butlerStr(args, "scope"), "all"))
			switch scope {
			case project.ScopeContainer, project.ScopeRepo, project.ScopeMetadata, project.ScopeAll:
			default:
				return "", "", fmt.Errorf("invalid scope %q: container, repo, metadata, or all", butlerStr(args, "scope"))
			}
			n := butlerSessionCount(ctx, d, id)
			sess := "1 session"
			if n != 1 {
				sess = fmt.Sprintf("%d sessions", n)
			}
			var blast string
			switch scope {
			case project.ScopeContainer:
				blast = fmt.Sprintf("This removes the container, its %s, and the home volume. The repo volume stays, and the project stays listed.", sess)
			case project.ScopeRepo:
				blast = fmt.Sprintf("This removes the container, its %s, and the repo volume (the code). The home volume stays, and the project stays listed.", sess)
			case project.ScopeMetadata:
				blast = "This removes only the project record, including its chats. Containers, volumes, and sessions are untouched."
			default:
				blast = fmt.Sprintf("This removes the container, its %s, both volumes, and the project record.", sess)
			}
			return fmt.Sprintf("Delete project %s?", id), blast, nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Projects == nil {
				return "", fmt.Errorf("no such project")
			}
			id := butlerStr(args, "project")
			scope := project.Scope(orDefault(butlerStr(args, "scope"), "all"))
			if d.Preview != nil {
				_ = d.Preview.Stop(ctx, id)
			}
			evictCDP(id)
			if err := d.Projects.Delete(ctx, id, scope); err != nil {
				return "", err
			}
			if d.Obs != nil {
				d.Obs.DeleteProject(id)
			}
			return "Deleted " + id, nil
		},
	},
	{
		name: butlerToolCreateHarness, desc: "Add a harness plugin to the registry.",
		schema: butlerSchema(map[string]any{"name": strProp(), "command": strProp(), "install": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			name, cmd := butlerStr(args, "name"), butlerStr(args, "command")
			if name == "" || cmd == "" {
				return "", "", fmt.Errorf("name and command are required")
			}
			return "Add harness " + name + "?", "Adds one registry entry. Nothing is installed yet.", nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Harnesses == nil {
				return "", fmt.Errorf("no harnesses")
			}
			id, err := d.Harnesses.Save(state.Harness{
				Name: butlerStr(args, "name"), Command: butlerStr(args, "command"), Install: butlerStr(args, "install"),
			})
			if err != nil {
				return "", err
			}
			return "Added " + id, nil
		},
	},
	{
		name: butlerToolInstallHarness, desc: "Install a harness into picked projects.",
		schema: butlerSchema(map[string]any{"harness": strProp(), "projects": map[string]any{"type": "array", "items": strProp()}}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			h := butlerStr(args, "harness")
			ids := butlerStrs(args, "projects")
			if h == "" || len(ids) == 0 {
				return "", "", fmt.Errorf("harness and at least one project are required")
			}
			return fmt.Sprintf("Install %s into %d projects?", h, len(ids)), "Runs its install command in each picked project.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Harnesses == nil || d.Sessions == nil || d.Projects == nil {
				return "", fmt.Errorf("not available")
			}
			h, err := d.Harnesses.Get(butlerStr(args, "harness"))
			if err != nil {
				return "", err
			}
			ok, skipped, failed := 0, 0, 0
			for _, pid := range butlerStrs(args, "projects") {
				st, cerr := d.Projects.EnsureContainer(ctx, pid)
				if cerr != nil || st.State != project.StateRunning {
					skipped++
					continue
				}
				if ierr := d.Sessions.InstallHarness(ctx, project.ContainerName(pid), h); ierr != nil {
					failed++
					continue
				}
				_ = d.Projects.RecordInstall(pid, h.ID)
				ok++
			}
			return fmt.Sprintf("ok=%d skipped=%d failed=%d", ok, skipped, failed), nil
		},
	},
	{
		name: butlerToolDeleteHarness, desc: "Remove a harness from the registry.",
		schema: butlerSchema(map[string]any{"id": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id := butlerStr(args, "id")
			if id == "" {
				return "", "", fmt.Errorf("id is required")
			}
			return "Delete harness " + id + "?", "Removes the registry entry. Installed binaries stay.", nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Harnesses == nil {
				return "", fmt.Errorf("no harnesses")
			}
			return "Deleted.", d.Harnesses.Remove(butlerStr(args, "id"))
		},
	},
	{
		name: butlerToolFanoutExec, desc: "Run one command in many projects. Example: update opencode everywhere.",
		schema: butlerSchema(map[string]any{"projects": map[string]any{"type": "array", "items": strProp()}, "command": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			ids, cmd := butlerStrs(args, "projects"), butlerStr(args, "command")
			if len(ids) == 0 || cmd == "" {
				return "", "", fmt.Errorf("projects and command are required")
			}
			return fmt.Sprintf("Run %q in %d projects?", cut(cmd, 60), len(ids)), "Runs once per picked project after Confirm. Skips stopped containers.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.Sessions == nil || d.Projects == nil {
				return "", fmt.Errorf("not available")
			}
			cmd := butlerStr(args, "command")
			ok, skipped, failed := 0, 0, 0
			for _, pid := range butlerStrs(args, "projects") {
				st, cerr := d.Projects.EnsureContainer(ctx, pid)
				if cerr != nil || st.State != project.StateRunning {
					skipped++
					continue
				}
				if _, rerr := d.Sessions.ExecCommand(ctx, project.ContainerName(pid), cmd); rerr != nil {
					failed++
					continue
				}
				ok++
			}
			return fmt.Sprintf("ok=%d skipped=%d failed=%d", ok, skipped, failed), nil
		},
	},
	{
		name: butlerToolProposeEnvFix, desc: "Name the missing variable. The UI collects the value in a masked field.",
		schema: butlerSchema(map[string]any{"name": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			name := butlerStr(args, "name")
			if name == "" {
				return "", "", fmt.Errorf("name is required")
			}
			return "Set " + name + "?", "The value is typed into a masked field, never shown in chat.", nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, secret string) (string, error) {
			name := butlerStr(args, "name")
			if strings.TrimSpace(secret) == "" {
				return "", fmt.Errorf("value is required — type it into the masked field")
			}
			// No persistence in v1: the value never touches logs, events, or
			// chat. The caller applies it in their environment and restarts.
			_ = d
			return "Noted " + name + " — set it in your environment and restart.", nil
		},
	},
	{
		name: butlerToolSwitchModel, desc: "Switch the model in one harness config. Proposes a one-line diff.",
		schema: butlerSchema(map[string]any{"harness": strProp(), "model": strProp()}),
		blast: func(d Deps, _ context.Context, args map[string]any) (string, string, error) {
			hid, model := butlerStr(args, "harness"), butlerStr(args, "model")
			if hid == "" || model == "" {
				return "", "", fmt.Errorf("harness and model are required")
			}
			if d.Harnesses == nil {
				return "", "", fmt.Errorf("no harness registry")
			}
			h, err := d.Harnesses.Get(hid)
			if err != nil {
				return "", "", err
			}
			where, old := "model", "(unset)"
			if path, prev, ok := butlerFindModel(h.Config); ok {
				where, old = strings.Join(path, "."), prev
				if !butlerAIModelExists(d.State, prev) {
					return "", "", fmt.Errorf("current model %q is not in the configured AI models (%s) — run list_ai_models for entry ids", prev, butlerAIModelNames(d.State))
				}
			}
			if !butlerAIModelExists(d.State, model) {
				return "", "", fmt.Errorf("model %q is not in the configured AI models (%s). Only switching is supported here — renaming an entry is update_ai_model", model, butlerAIModelNames(d.State))
			}
			return fmt.Sprintf("Switch model in %s to %s?", hid, model),
				fmt.Sprintf("Rewrites one line in %s config: %s %s -> %s.", hid, where, old, model), nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.State == nil {
				return "", fmt.Errorf("no state")
			}
			hid, model := butlerStr(args, "harness"), butlerStr(args, "model")
			err := d.State.Mutate(func(doc *state.Document) error {
				h, ok := doc.Harnesses[hid]
				if !ok {
					return fmt.Errorf("no such harness")
				}
				h.Config = butlerSetModel(h.Config, model)
				doc.Harnesses[hid] = h
				return nil
			})
			if err != nil {
				return "", err
			}
			return "Switched to " + model, nil
		},
	},
	{
		name: butlerToolUpdateAIModel, desc: "Rename or relabel one AI model entry by id. Proposes a one-line diff.",
		schema: butlerSchema(map[string]any{"id": strProp(), "label": strProp()}),
		blast: func(d Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, label := butlerStr(args, "id"), butlerStr(args, "label")
			if id == "" || label == "" {
				return "", "", fmt.Errorf("id and label are required")
			}
			if d.State == nil {
				return "", "", fmt.Errorf("no state")
			}
			var old string
			found := false
			d.State.View(func(doc *state.Document) {
				if i := aiIndex(doc, id); i >= 0 {
					old, found = doc.AIModels[i].Label, true
				}
			})
			if !found {
				return "", "", fmt.Errorf("no such AI model entry %q — run list_ai_models for the ids", id)
			}
			// Display name (label, falling back to the model string) in the
			// summary; the opaque minted id only disambiguates in the blast.
			disp := func(s string) string {
				if s == "" {
					return id
				}
				return s
			}
			return fmt.Sprintf("Rename AI model %q to %q?", disp(old), label),
				fmt.Sprintf("Rewrites the label of model entry %s (current model string %s): %q -> %q. The model name, endpoint, and stored key stay untouched.", id, dispButlerAIModelString(d, id), disp(old), label), nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.State == nil {
				return "", fmt.Errorf("no state")
			}
			id, label := butlerStr(args, "id"), butlerStr(args, "label")
			err := d.State.Mutate(func(doc *state.Document) error {
				i := aiIndex(doc, id)
				if i < 0 {
					return fmt.Errorf("no such AI model entry %q", id)
				}
				doc.AIModels[i].Label = label
				return nil
			})
			if err != nil {
				return "", err
			}
			_, _ = d.Events.Append("ai.model_updated", map[string]any{"id": id, "label": label})
			return "Renamed to " + label, nil
		},
	},
	{
		name: butlerToolSaveShortcut, desc: "Store a command or keys shortcut for one project.",
		schema: butlerSchema(map[string]any{"project": strProp(), "alias": strProp(), "kind": strProp(), "command": strProp(), "keys": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, alias, kind := butlerStr(args, "project"), butlerStr(args, "alias"), butlerStr(args, "kind")
			if id == "" || alias == "" || (kind != "cmd" && kind != "keys") {
				return "", "", fmt.Errorf("project, alias, and kind (cmd|keys) are required")
			}
			return fmt.Sprintf("Save shortcut %s in %s?", alias, id), "Adds or replaces one shortcut row.", nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.State == nil {
				return "", fmt.Errorf("no state")
			}
			row := state.Shortcut{
				Alias: butlerStr(args, "alias"), Kind: butlerStr(args, "kind"),
				Command: butlerStr(args, "command"), Keys: butlerStr(args, "keys"),
			}
			if err := validateShortcuts([]state.Shortcut{row}); err != nil {
				return "", err
			}
			if row.ID == "" {
				row.ID = "s-" + row.Alias
			}
			id := butlerStr(args, "project")
			err := d.State.Mutate(func(doc *state.Document) error {
				p, ok := doc.Projects[id]
				if !ok {
					return fmt.Errorf("no such project")
				}
				kept := false
				for i, s := range p.Shortcuts {
					if s.Alias == row.Alias {
						p.Shortcuts[i] = row
						kept = true
					}
				}
				if !kept {
					p.Shortcuts = append(p.Shortcuts, row)
				}
				doc.Projects[id] = p
				return nil
			})
			if err != nil {
				return "", err
			}
			return "Saved " + row.Alias, nil
		},
	},
	{
		name: butlerToolSaveGitIdentity, desc: "Apply a stored git identity into one project by id.",
		schema: butlerSchema(map[string]any{"project": strProp(), "identityId": strProp()}),
		blast: func(d Deps, _ context.Context, args map[string]any) (string, string, error) {
			id, gid := butlerStr(args, "project"), butlerStr(args, "identityId")
			if id == "" || gid == "" {
				return "", "", fmt.Errorf("project and identityId are required")
			}
			name := gid
			if d.State != nil {
				d.State.View(func(doc *state.Document) {
					for _, g := range doc.GitIDs {
						if g.ID == gid {
							name = g.Name + " <" + g.Email + ">"
						}
					}
				})
			}
			return fmt.Sprintf("Set git identity in %s to %s?", id, name), "Runs git config user.name/email in that repo.", nil
		},
		exec: func(ctx context.Context, d Deps, args map[string]any, _ string) (string, error) {
			container, dir, err := butlerContainer(ctx, d, butlerStr(args, "project"))
			if err != nil {
				return "", err
			}
			var name, email string
			if d.State != nil {
				d.State.View(func(doc *state.Document) {
					for _, g := range doc.GitIDs {
						if g.ID == butlerStr(args, "identityId") {
							name, email = g.Name, g.Email
						}
					}
				})
			}
			if name == "" || email == "" {
				return "", fmt.Errorf("unknown identity")
			}
			qd := shellQuote(dir)
			_, xerr := d.Sessions.ExecCommand(ctx, container,
				"git -C "+qd+" config user.name "+shellQuote(name)+" && git -C "+qd+" config user.email "+shellQuote(email))
			if xerr != nil {
				return "", xerr
			}
			return "Set " + name, nil
		},
	},
	{
		name: butlerToolAddSSHKey, desc: "Register an SSH public key.",
		schema: butlerSchema(map[string]any{"publicKey": strProp(), "label": strProp()}),
		blast: func(_ Deps, _ context.Context, args map[string]any) (string, string, error) {
			if _, err := sshkeys.Check(butlerStr(args, "publicKey")); err != nil {
				return "", "", fmt.Errorf("publicKey is required and must look like an SSH public key")
			}
			return "Add this SSH key?", "Registers one public key. The private key never leaves your machine.", nil
		},
		exec: func(_ context.Context, d Deps, args map[string]any, _ string) (string, error) {
			if d.SSHKeys == nil || d.State == nil {
				return "", fmt.Errorf("no ssh store")
			}
			var email string
			d.State.View(func(doc *state.Document) { email = doc.User.Email })
			fp, err := d.SSHKeys.Add(email, butlerStr(args, "publicKey"), butlerStr(args, "label"))
			if err != nil {
				if err == sshkeys.ErrDuplicateKey {
					return "", fmt.Errorf("key already registered")
				}
				return "", err
			}
			return "Added " + fp, nil
		},
	},
}

// butlerStrs decodes a string array arg.
func butlerStrs(args map[string]any, key string) []string {
	var out []string
	switch v := args[key].(type) {
	case []any:
		for _, e := range v {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		if strings.TrimSpace(v) != "" {
			out = append(out, strings.TrimSpace(v))
		}
	}
	return out
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// butlerAIModelNames returns the configured model names ("Model" field,
// what switch_model validates against), for error context so the model
// can self-correct instead of guessing.
func butlerAIModelNames(st *state.Store) string {
	if st == nil {
		return "none configured"
	}
	names := []string{}
	st.View(func(doc *state.Document) {
		for _, m := range doc.AIModels {
			if s := strings.TrimSpace(m.Model); s != "" {
				names = append(names, s)
			}
		}
	})
	if len(names) == 0 {
		return "none configured"
	}
	return strings.Join(names, ", ")
}

// dispButlerAIModelString returns the model string ("Model" field) of
// one entry by id, for display in summaries.
func dispButlerAIModelString(d Deps, id string) string {
	if d.State == nil {
		return "(unset)"
	}
	s := "(unset)"
	d.State.View(func(doc *state.Document) {
		if i := aiIndex(doc, id); i >= 0 {
			if v := strings.TrimSpace(doc.AIModels[i].Model); v != "" {
				s = v
			}
		}
	})
	return s
}

func butlerAIModelExists(st *state.Store, name string) bool {
	if st == nil || strings.TrimSpace(name) == "" {
		return false
	}
	found := false
	st.View(func(doc *state.Document) {
		for _, model := range doc.AIModels {
			if strings.TrimSpace(model.Model) == name {
				found = true
				return
			}
		}
	})
	return found
}

// butlerFindModel locates the first "model" string in a harness config
// blob, descending sorted keys for determinism. Profile-shaped configs
// (providers.openai.model) report their dotted path; flat ones report
// ["model"]. ok is false when no model string exists.
func butlerFindModel(raw json.RawMessage) (path []string, old string, ok bool) {
	var m map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil, "", false
	}
	var walk func(prefix []string, obj map[string]any) bool
	walk = func(prefix []string, obj map[string]any) bool {
		keys := make([]string, 0, len(obj))
		for k := range obj {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			switch v := obj[k].(type) {
			case string:
				if k == "model" && strings.TrimSpace(v) != "" {
					path, old, ok = append(prefix, k), v, true
					return true
				}
			case map[string]any:
				if walk(append(prefix, k), v) {
					return true
				}
			}
		}
		return false
	}
	walk(nil, m)
	return path, old, ok
}

// butlerSetModel returns the config blob with the model set: at its
// existing path when one is found, top-level "model" otherwise.
func butlerSetModel(raw json.RawMessage, model string) json.RawMessage {
	var m map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &m)
	}
	if m == nil {
		m = map[string]any{}
	}
	if path, _, ok := butlerFindModel(raw); ok && len(path) > 0 {
		cur := m
		for _, k := range path[:len(path)-1] {
			next, _ := cur[k].(map[string]any)
			if next == nil {
				next = map[string]any{}
				cur[k] = next
			}
			cur = next
		}
		cur[path[len(path)-1]] = model
	} else {
		m["model"] = model
	}
	out, _ := json.Marshal(m)
	return out
}
