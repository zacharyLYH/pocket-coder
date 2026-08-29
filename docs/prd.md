# Product Requirements — The Codespaces Mental Model

> Self-hosted GitHub Codespaces, minus the IDE. The browser is the terminal. CLI coding harnesses replace the editor. The user flow is already well understood, so we copy it and ship it dumb.

This is the source of truth for the product.

---

## 1. What this is

Codespaces taught the world that:

- a repo becomes an isolated cloud environment in one motion
- the terminal is a first-class surface
- any listening port becomes a clickable URL
- environments stop, start, rebuild, and reconnect cleanly
- secrets are env vars, injected, never shown
- you own your data when you self-host

This product is that pattern, with two differences:

1. **No IDE.** CLI coding harnesses (opencode, freebuff, cline, anything) replace the editor. They run in persistent terminal sessions.
2. **No vendor.** Self-hosted only. Not a SaaS. Users run it on a Mac, home server, or VPS. They own all compute, repos, credentials, and data.

The user flow and expectations are inherited from Codespaces. Nothing gets reinvented.

### Concept map

Each Codespaces concept maps 1:1 here.

| GitHub Codespaces | Side Project Saviour |
|---|---|
| Create a codespace (repo + branch) | Create a project (repo + branch, or blank) |
| devcontainer.json builds the environment | a shared sandbox image; users install what they need inside |
| Open in browser | terminal-driven web UI, mobile-first |
| Terminal | tmux-backed persistent terminal |
| Ports panel + forwarded URLs | Preview tab: ports list + reverse proxy |
| Stop a codespace (compute freezes, files persist) | stop the container; tmux dies, volumes persist |
| Rebuild a codespace | rebuild the image from the repo Dockerfile |
| Reopen and land where you left off | re-attach to the project's tmux sessions |
| Codespaces secrets | masked env editor |
| One isolated codespace per repo | one isolated container per project |

Rule: adopt these expectations verbatim. If a concept is not in the map, it does not earn a first-class feature.

| Codespaces workflow | Behavior here |
|---|---|
| Create → building indicator → ready | create project → sandbox container runs → clone lands in `/workspace/repo` → "Ready" |
| Any listening port becomes a forwarded URL | detect ports in the container; each becomes a Preview link |
| Secrets set once, injected everywhere | env editor, masked, written to a `0600` file, sourced by login shells |
| Closed laptop ≠ lost work | browser disconnect kills the view; tmux keeps the work |
| Stop = pause, not delete | stop never touches the repo volume |
| Delete = explicit and destructive | delete requires confirmation; scoped to container / metadata / repo |

---

## 2. Architecture

Boring primitives only: Docker, tmux, Git, WebSockets, JSON files, HTTP.

```
Browser (phone/desktop, terminal-driven)
   │  WebSocket + REST
   ▼
Go server ───────► Docker Engine (unix socket)
   │
   ├── container: project-A   (sandbox: repo clone, tmux, harnesses, services)
   ├── container: project-B   (isolated)
   └── container: project-C   (isolated)
```

- The server never executes anything itself. It asks Docker (`docker exec`) and streams the TTY. The container is the security boundary.
- One isolated container per project.
- Storage: one JSON file per project plus the cloned repo.
- Sessions are discovered from tmux. They are never stored.
- Backend deps: `go-dockerclient` + `coder/websocket`.
- Frontend: React + xterm.js.
- That is the entire dependency story.

### System authentication

The only reason for system login is safe access to the self-hosted box over the internet.

- Set once, in `.env` (`SPS_LOGIN_EMAIL`): the email that receives login codes.
- Log in: enter the email → a one-time PIN is emailed → enter the PIN → the browser gets a JWT.
- Every request and WebSocket carries the JWT. A middleware checks it.
- This is separate from harness auth (LLM credentials, see Harness auth).

### Persistence

- Docker volumes hold system config and per-project state: repo, env, harness files, and the container home volume (tmux sessions, harness state).
- Server restart keeps all state. Running sessions may terminate; reopen the project to relaunch them.
- Moving to a new machine loses state. The user exports all data and imports it on the new box. Export/import is deferred (see Deferred).

---

## 3. Core concepts

| Concept | Meaning |
|---|---|
| Project | a git repo, cloned into an isolated environment |
| Environment | a sandbox container from the shared sandbox image; holds code, deps, tools, env, processes |
| Session | a persistent tmux window; a shell, or a harness, or a command |
| Harness | a CLI plugin, launched in a session |
| Action | a button: `{ label, command }`, run in a session |
| Preview | forwarded ports, reverse-proxied to the browser |
| Diff | git change list, rendered as a list |
| Env | masked secrets, injected into containers |

---

## 4. User flow

### First run

1. Create a `.env` file with the email that receives login codes plus the SMTP credentials (see the README). Then install and start the server. One command.
2. Log in: enter the email, receive a PIN by email, enter it. The browser gets a JWT.
3. You land on the projects screen. Empty? "Clone a repo →".
4. Default harnesses (terminal, opencode, freebuff, cline) are already in "+ New Session". Nothing to configure first.
5. Clone a repo (or start blank) → "Ready" in seconds. Open it.
6. The first time you launch a harness that needs credentials, auth happens right then (see Harness auth). Later launches are one tap.

No gate. The user is useful before configuring anything.

### Daily loop

1. Open a project. You see tabs, not pages.
2. Tap a harness to start an engineer. Or tap a button to run a command. Or open the terminal and type.
3. Check the Preview port that the engineer's dev server opened.
4. Check the Diff to review changes.
5. Ask the secretary for the mobile-hard work: keys, models, buttons, "where do I stand?"
6. Close the phone. The sessions keep running.

### Reconnect

1. Reopen the project. Re-attach to the same tmux sessions.
2. Land exactly where you left off.
3. "Brief me." The secretary reads what's running and summarizes where things stand.

The whole point: prompt your LLM from a phone, anytime.

---

## 5. Harnesses

A harness is a plugin file. Our defaults and yours are the same thing.

```jsonc
// $DATA_DIR/harnesses/my-agent.json
{ "name": "My Agent", "command": "my-agent", "install": "npm i -g my-agent" }
```

- Builtins are the same files. On first start the platform seeds `$DATA_DIR/harnesses/` with terminal, opencode, freebuff, and cline. They are real, editable files. Self-hosted means hackable.
- Adding a harness is global, not per-project. Drop one file in `$DATA_DIR/harnesses/` and it appears in every project's "+ New Session".
- The "Add harness" form (name + command + optional install) writes the file for you. The form and the folder are the same thing.
- Optional `install` runs in the container the first time the harness is launched. A missing `command` self-heals instead of failing.

### It must be a CLI

The platform verifies before accepting a harness. After install, it runs `command --version` (or `--help`) inside a container with a forced TTY and a few-second timeout.

- A real CLI prints something and exits.
- An IDE or GUI needs a display, hangs, or prints a display error. The form rejects it: "not a CLI — it looks like it wants a display (an IDE/GUI), and the platform only runs terminal programs."
- The same rule is enforced at runtime. Every harness launches inside a TTY in the container.

### Launch

```
+ New Session → My Agent
  → docker exec tmux new-session -d -s my-agent-1 -c /workspace/repo \
        'my-agent || echo "[my-agent exited: $?]"'
  → browser attaches to the tmux window
```

The `|| echo` is the failure story. A harness died, you see it, you hit **Restart**. No exit-code plumbing. No orchestrator. No vigil over the agents.

---

## 6. Commands are buttons

On a phone, typing into a terminal is awkward. Commands become buttons. A button is `{ label, command }`. Tapping it runs the command in a session and streams output to the terminal.

- Defaults are detected, not configured. A few boring checks on the repo produce the common buttons: `package.json` scripts → "Start dev server" / "Run tests", a Makefile → "make test", a `go.mod` → "go test". Detection finds nothing → you still get the harness launcher and a terminal. Nothing blocks.
- Custom buttons live in the project file: `actions: [{ "label": "Start API", "command": "npm run dev:api" }]`.
- The launcher and the buttons are the same primitive. Every "+ New Session" entry and every action button is `{ label, command }`. Harnesses are the global set of buttons. Actions are the per-project set.
- Add your own: next to the buttons, a small "+ Add" opens a two-field form (label, command). It writes the project file. Power users edit the file directly. Same pattern as harnesses.
- There is no `start` command. Long-running commands are buttons you can re-tap. Preview comes from port detection: whatever is listening in the container becomes a link.

---

## 7. Harness auth

This is harness auth: the LLM credentials your agents use. System login is separate (see System authentication).

A harness without credentials cannot talk to any LLM. Auth is how this product works at all.

- **API keys via env** is the primary path. Add `ANTHROPIC_API_KEY`, launch any harness, done.
- **Device-flow login** — CLIs that print a URL + code. Complete the login in the terminal tab, from the phone. No SSH needed.
- **Credential upload** — a last resort for CLIs with neither.
- Auth metadata lives in the harness plugin file. Builtins ship their `auth` block. A custom plugin carries the same one. Same schema, same chips, same code path.
- Auth is on-demand, never a gate. The first time you launch a harness that needs credentials, you are walked through it right then. Later launches are one tap. The chip says what's missing and points at the fix.

---

## 8. Environments and configuration

- The environment is a **sandbox**: one shared image with the boring essentials (git, tmux, `ss`, SSH, CA certs). No per-repo Dockerfile, no build step — a project is ready in seconds, and nothing about the user's app needs to be dockerizable.
- Users install whatever they want inside their sandbox (`apt-get`, `npm i -g`, ...). Installs are isolated per container. Installs into `$HOME` survive container recreation (home volume); system-wide installs survive stop/start but not recreation — that tradeoff is surfaced in the UI, not hidden.
- Config never goes into the repo. We do not write into the user's source tree.
- Harnesses are not in the project file. They live globally in `$DATA_DIR/harnesses/`.
- The per-project file (`$DATA_DIR/projects/<id>/project.json`, `0600`) holds only what cannot be defaulted:
  - `name`, `repo`, `branch` — defaults cover name and branch
  - `actions` — custom buttons; defaults are auto-detected
  - `env` — secrets, masked in the UI
- Everything else is a default. A brand-new file means defaults only.

---

## 9. Preview and diff

- **Preview** is a ports panel. The container's listening ports are detected. Each becomes a reverse-proxied link. Nothing to configure.
- Preview picks up whatever the engineer spawns. Have a harness start the dev server in its session, and the listening port becomes a link. No config, no start step.
- **Diff** is git. A list of changed files with added/deleted counts, plus a raw view. Git stays fully usable through the terminal and harnesses.

---

## 10. Mobile is the whole point

The thesis: prompt your LLM on the move.

- Reopen a project and re-attach. Land where you left off.
- The surface is deliberately small: project list, session tabs, a terminal that resizes and reconnects, a preview button, a diff list.
- Chat typing is the easy primitive on mobile. Terminals, configs, and file editing are not. The whole UI is organized around that gap: buttons for commands, chat for everything else.
- **Creature-comfort buttons on the terminal** — a fixed overlay in the key strip, always visible:
  - **↑ repeat last command** — mobile keyboards have no up-arrow and every developer lives on shell history
  - **Ctrl-C, Ctrl-L (clear), Tab (completion), Esc, ←/→**
  - One tap sends the keystrokes into the session. These are buttons, not platform features.
- On-screen key strip, desktop-width mode for TUIs, copy/paste.
- No TUI redesign. No native mobile UIs. The terminal is the mobile UI.

---

## 11. The secretary

AI-native, done bounded.

### The office

- **You are the boss.** You orchestrate. You pick the harness, decide what gets built, accept or reject suggestions.
- **Your harnesses are the engineers.** They do the engineering: code, tests, git. Inside the repo, inside their sessions.
- **The secretary is the project helper.** It maintains the environment: buttons, harness plugins, harness config (which model, which key), UX config, summaries. Never engineering.

The secretary is a **dumb bot**. It does exactly what you ask, nothing on its own. You never wonder what it's doing behind your back.

### Why it exists

Chat is the mobile workaround. You say what you want in chat. The secretary does the mobile-hard part: config edits, wiring, file surgery. It shows a confirmable diff. It never touches the engineering, but it handles everything about the environment you would otherwise poke at with a terminal and `vi` from a phone.

### Credentials

No separate setup. The secretary rides the credentials you already authenticated.

- Once at least one CLI can talk to an LLM, the secretary uses the same provider and key.
- A dedicated key is an optional override, never a requirement.

### Tools and skills

The secretary is not a text box. It has real hands, for environment work only:

- **Read** — project.json, harness plugin files, harness config files in the container's home volume, container state (what's running, what's listening, auth-chip status), and the event log (§12) — what happened and why.
- **Apply** — confirmed edits to the same three config categories, via the container file primitive.
- **Probe** — auth probes and port probes, when asked.

It has no skill for editing your repo, running engineering commands, or watching sessions. Those skills are not installed.

### How it's taught

A shipped **system guide** (plain markdown, editable by users) describes the architecture in LLM-shaped terms: the schemas, the button model, the preview model, the diff view. For each builtin harness it documents the config file and the exact model/knob field, so the secretary can switch models or wire keys without guessing. The bounds are the guide. Data, not code. A curious owner can extend what the secretary knows.

### Abilities — v1

You ask, it acts. Nothing unsolicited.

- **Wire up my key.** "I bought an Anthropic key, get it working everywhere." It knows which env var each harness reads and which harnesses are missing it. Secret values never pass through chat. It hands you a one-tap masked field per harness. You paste. The masked env editor holds it. It names the right variable and confirms the auth chip flipped.
- **Switch me to model B.** "Use Sonnet for opencode, not Opus." It reads the harness config, proposes the one-line change as a diff. Confirm → written.
- **Suggest buttons.** "What buttons would help me here?" It reads the repo, current actions, and (on request) a slice of recent session output. It proposes `{ label, command }` buttons. Accept what you want.
- **Where do I stand?** A briefing on the projects screen. What changed, what's running, what's red, what's waiting on you. The whole portfolio from a phone.

### Abilities — later, within the same bounds

- **Edit UX properties on request, inside the config schema.** Reorganize buttons. Rename a label. Add a harness plugin file. Across any project. Every edit is a shown diff, confirmed, then written.
- **Harness pairing.** "Which harness fits this task?" The answer is a harness plugin file.
- **Brief me.** A short "what happened while I was away." It reads what's running now and looks at output you ask it to. Still on-request.

### Bounds, non-negotiable

1. A dumb bot. Acts only when asked.
2. Credentials come from your CLIs. Optional override key, never a requirement.
3. Suggest, confirm, apply. Nothing is written without an explicit per-edit confirm. Never edits live state.
4. No engineering ever. Never a repo, never code. Config files only: project.json, harness plugins, and harness config files in the container's home volume. Env values are never part of that set.
5. Secret values never enter chat. A pasted key lands in a masked field in the same tap. The secretary only sees env var names. Values never leave the box except when a harness sends them to the LLM provider you chose.
6. Stateless. Per-request. No queued jobs, no polling, no residency.

---

## 12. Observability

Developers will want to know what their system is doing. Observability is for the operator of the box: understanding your own tool and your own usage. Nothing leaves the box.

It is also a **development aid, present from day 1**: events are written while we build the thing, so the system becomes self-explanatory exactly when it's being debugged — by you and by the secretary.

### How we collect

Nothing is telemetry. Each surface collects from the source that already owns the fact — the server, Docker, or the harness itself. Three mechanisms, no pipeline:

1. **Event log** — the server's own audit trail. One JSONL line per fact the server caused or detected: login, project created/stopped/deleted, image built, session attach/detach, action run, env changed, and `error` lines. `$DATA_DIR/events.log`, append-only, read API in Phase 2. Reading it *is* history.
2. **On-demand reads** — the current state of Docker and the containers, queried when a page loads and polled while it's open: `docker stats`/inspect for live state, `tmux list-sessions` for sessions, `docker logs` for container output, port probes for preview, disk/uptime for health. No persistence — the page paints whatever the world looks like now.
3. **The harnesses' own records** — harnesses already record usage: opencode keeps its sessions and message history in its storage; cline keeps its conversation history and commits; freebuff similar. We read those files from the container's home volume (each builtin documents where, in the system guide). Server-side counts are the fallback, never the source of truth for what an agent did.

The page **lives at the home screen, outside any project**: a top-level item to click into, not a project tab. It is the system view.

### What gets surfaced, and how it's collected

| Surface | Collected by | Collected when |
|---|---|---|
| Live state | on-demand reads: `docker stats`/inspect, `tmux list-sessions`, port probes | page load, then polled while open |
| Usage — your workflow | event-log aggregates for *server activity* (launches, actions, session age) + harnesses' own records for *agent activity* (conversations, history) | read-time |
| Logs | server log tail; `docker logs` per container | read-time, filterable |
| Errors | `error` event lines + harness exit codes (the session wrapper already captures `$?`) | written at the moment, surfaced read-time |
| Auth status | harness `auth` blocks + on-demand CLI/config probes | read-time |
| Health | docker ping, disk free on `$DATA_DIR`, server version, uptime | page load |

No Prometheus, no Grafana, no telemetry pipeline, no SaaS. One page in the browser, boring sources. The event log is exportable by copying the file; live state is not persisted, by design.

### The secretary's debug source

The same three sources are read-only probes for the secretary (§11). "Why did the build fail?" reads the event log; "what's actually running?" reads the containers; "what did the agent do?" reads the harness's own records. No special instrumentation — the sources already exist.

---

## 13. Deferred, not deleted

Cut to "later, when a user actually hits the wall":

- **Import/export.** Not built now. It is the future substrate for a repeatable experience: set up a machine once, restore projects anywhere. Export would be a portable snapshot (config + env names, never values) plus a `tar.gz` of the data dir. Import would recreate projects, re-enter secrets once, relaunch sessions. For v1, backup is simple: `$DATA_DIR` is one folder. Copy it.
- **Harness version pins** and an admin update page. Pin via setup if you care.
- **Headless-browser policy** and hard-blocks.
- **Admin screen polish.** The observability page ships as tables + the raw event log. Pretty graphs and CSV/JSON export formats are later.
- **Image-tag versioning machinery.**

---

## 14. V1 scope

- [ ] System auth: email + one-time PIN → JWT, middleware on all requests
- [ ] First-run: log in with email+PIN → land on projects → clone → ready; harness auth is on-demand
- [ ] Persistence: docker volumes for system config + per-project state; restart keeps state
- [ ] Create project from repo → clone in sandbox → "Ready" (blank sandboxes allowed too)
- [ ] Terminal tab: xterm.js ↔ tmux via ws, resize, reconnect
- [ ] Creature-comfort buttons: ↑ repeat-last-command, Ctrl-C, Ctrl-L, Tab, Esc
- [ ] Global harness plugins: builtins seeded as files + "Add harness" form, one code path
- [ ] Action buttons: detected defaults + custom `actions`, run in tmux from a tap
- [ ] Preview tab: ports panel, detected ports, reverse-proxied URLs
- [ ] Diff list + raw view
- [ ] Masked env editor (auth via API keys)
- [ ] Device-flow login for interactive-CLI harnesses
- [ ] The secretary: rides an authed CLI's credentials, dumb-bot chat + bounded config toolset, confirm-apply
- [ ] Observability: append-only event log; live state, usage summary, log tails, errors, auth status, health
- [ ] Self-host deployment: `docker compose up`, one volume

### Litmus test

This product works if this feels natural:

> I have a Go project running on my home server. I open it from my phone. I tap Freebuff and tell it to fix something. I close my phone. Two hours later I come back, inspect the diff, notice Freebuff is rate-limited. I open OpenCode in another tab and tell it to continue. I check the web preview. I push the result.

If that workflow is smooth, we have a product.