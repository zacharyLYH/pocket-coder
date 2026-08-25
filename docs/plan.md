# Implementation Plan — Waterfall Work Packages

> Source of truth: `docs/prd.md`. Each phase below is a gated, medium-sized deliverable. Each ends with a demo and tests that pass on a laptop with only Docker installed. No phase starts until the previous phase's gate passes.

## How we keep everything locally testable

- The server runs on the host in dev (`make dev`) against the host Docker engine. Project containers are real containers on that same engine — siblings, not nested. The prod "server image ships project images" path (mounted socket) is exercised by `make e2e`, which runs the *same* flow through compose.
- Tests drive real Docker, real tmux, real git — wrapped in thin internal packages, never mocked at the boundary. Parsing/logic unit tests need no Docker.
- `test/fixtures/` ships tiny Dockerfiles (alpine+node), a sample "hello" repo, and fake CLIs so harness flows don't need real network installs.
- Email in dev prints the PIN to the server log (a `Mailer` interface; the SMTP implementation sends via Google SMTP with `net/smtp` using an app password from `.env`). No SMTP server to run anywhere — the credentials are just `SMTP_*` env vars.
- Every phase adds a `make check` step next to `make dev` / `make test`.

## Observability is a day-1 concern

Three collection mechanisms, no metrics pipeline:

1. **Event log** (`$DATA_DIR/events.log`, append-only JSONL, `GET /api/events`) — the server's own audit trail. Every phase emits what it causes: login, create/stop/delete, build, session attach/detach, action run, env change, error. Development debugging *is* reading it; the build is exercised through it.
2. **On-demand reads** — the current state of Docker and the containers, queried at page load and polled while open: stats/inspect, `tmux list-sessions`, `docker logs`, port probes, disk/uptime. No persistence.
3. **Harness-native records** — harnesses record their own usage (opencode's storage, aider's history, freebuff's). We read those files from the container's home volume; server-side counts are the fallback only. Phase 8 documents where each builtin records usage; Phase 12 harvests it; Phase 13's secretary reads all three sources for debugging.

Placement: a top-level item on the home screen, outside any project — the system view, not a project tab.

## Phases at a glance

| # | Phase | Core deliverable | Demonstrable with |
|---|---|---|---|
| 1 | Foundations & control loop | repo layout, config, health, dev compose, Vite scaffold | `curl /health` |
| 2 | Storage & registry primitives | data dir, project.json CRUD, harness plugin loader, seeding | unit tests + `make dev` boot |
| 3 | Docker control plane | thin go-dockerclient wrapper (build/run/exec/resize/cp/logs/net) | integration test spins throwaway container |
| 4 | System authentication | email + PIN → JWT, middleware on all routes/WS | login prints PIN to logs |
| 5 | Live hosting & CI/CD | Netcup VPS + DuckDNS + HTTPS; live-check at every step; deploy-on-push | `curl https://.../health` from a laptop |
| 6 | Project create pipeline | sandbox container per project, clone inside, ready in seconds | fixture repo → "Ready" via curl |
| 7 | Terminal transport | WS ↔ tmux via exec hijack + resize + reconnect | typed into a browser terminal |
| 8 | Harnesses & sessions | session CRUD, CLI validation, builtins, launch/restart | fake CLI harness session |
| 9 | Preview & diff | port detect + reverse proxy + ports panel; git diff API+UI | http.server in container → URL |
| 10 | Env editor & persistence | masked env, login-shell injection, volume survival | `$VAR` after restart |
| 11 | Frontend completion & buttons | full UI, action buttons, creature-comfort keys, mobile | litmus test on a phone |
| 12 | Packaging & self-hosting + observability | server image, compose, quickstart, observability page | fresh box via `docker compose up` |
| 13 | The secretary (v1) | chat, system guide, tools, confirm-diff, wire-key/switch-model | the 4 v1 abilities on a fixture |

---

## Phase 1 — Foundations & control loop

**Goal.** A bo-repo that builds, boots, and answers health. Nothing more.

**Scope.**
- Repo layout: `server/` (Go: `cmd/server`, `internal/{config,auth,docker,project,session,terminal,preview,diff,harness}`), `web/` (TS), `test/fixtures`.
- Config via env/flags: `DATA_DIR`, `BIND`, `LOGIN_EMAIL`, `JWT_SECRET`, `SMTP_*`, `DOCKER_SOCK`.
- `Go server` skeleton: logging, graceful shutdown, `GET /health` + `GET /api/ping`.
- `internal/config` loads and validates; unknown config fails loudly.
- `Makefile`: `make dev`, `make test`, `make build`, `make check`, `make e2e` (stub).
- `docker-compose.dev.yml`: boots the server and the Vite dev server proxying `/api` and `/ws`; SMTP credentials come from `.env` (Google app password, `net/smtp` in Phase 4) — no SMTP sink.
- `web/` Vite + React + TS scaffold rendering a placeholder page.

**Gate (test locally).** `docker compose -f docker-compose.dev.yml up`; `curl /health` returns 200; `make test` runs the config unit tests; server exits clean on SIGTERM.

---

## Phase 2 — Storage & registry primitives

**Goal.** The data dir is the entire database. Files, not tables.

**Scope.**
- Bootstrap `$DATA_DIR/{projects,harnesses,ssh}` with correct perms on first boot.
- `project.json` CRUD with atomic writes (temp + rename). Fields per PRD §8: `name/repo/branch/actions/env` — file mode `0600`.
- Harness plugin loader: scan `$DATA_DIR/harnesses/*.json`, validate schema (`name/command/install/auth`), parse `auth` block. One code path for builtins and user files.
- Seed builtins (terminal, opencode, freebuff, aider) on first boot as real editable files.
- Projects index (`$DATA_DIR/projects.json`) so the list renders without container scans.
- Append-only event log (`$DATA_DIR/events.log`, JSONL) with a typed `Event` writer and `GET /api/events?after=<id>&limit=` (tail/pagination); the UI and the secretary both read history from it.
- Every later phase emits lines (login, project create/stop/delete, session create/exit, action run, harness launched, error) and exercises them in its gate.
- `weathervane` package for live container state — not needed until Phase 6; add stub.

**Gate.** Unit tests for CRUD atomicity, loader, schema rejection, seeding idempotence (`seeded twice = same files`). Read API returns appended lines with correct pagination. `make dev` shows the seeded tree in logs.

---

## Phase 3 — Docker control plane

**Goal.** A thin, boring wrapper over `go-dockerclient`. The server's whole interaction with Docker lives here.

**Scope.**
- Image build with streamed logs (the shared sandbox image needs this).
- Container create/start/stop/remove/inspect with defaults: non-privileged, read-only rootfs where practical, resource limits, `sps-net` network, named volumes (`sps-<id>-repo`, `sps-<id>-home`).
- Exec: `docker exec` with `-i -t`, attach hijack, and `ResizeExecTTY`.
- Logs tail. File upload (the `docker cp` primitive) for env/harness config writes. Port detection (`ss -tlnp`). SSH-key mount into containers.
- All functions return typed errors; no raw docker API leaks past this package.

**Gate.** Integration test (run via `make docker-test`, tagged `integration`): builds a tiny fixture image, runs it, execs `echo`, uploads a file, inspects it, removes it. Fails fast (not silently) when Docker is unavailable, with a clear message.

---

## Phase 4 — System authentication

**Goal.** The only reason login exists is safe internet exposure. Keep it minimal: email + one-time PIN → JWT.

**Scope.**
- `Mailer` interface with two impls: `ConsoleMailer` (dev: print PIN to log) and `SmtpMailer` (sends via Google SMTP with `net/smtp`, creds from `SMTP_*` env vars — an app password, no SMTP server).
- `SPS_JWT_SECRET` is not a required env var: on first boot generate a random secret with `crypto/rand` and persist it to `$DATA_DIR` (0600); the env var overrides. A hardcoded default secret would be a security hole.
- Flow: request PIN for the configured `SPS_LOGIN_EMAIL` (set in `.env`, not a startup prompt) → rate-limit and expire PINs → verify → issue signed JWT (crypto/rand, expiry, `HttpOnly; SameSite=Strict` cookie).
- Middleware: every `/api/*` and `/ws/*` route requires a valid JWT (except login/PIN routes). WebSocket handshake validates the cookie too.
- Logged-out and logged-in UI states in the scaffolded frontend.
- Emit `login` events (success/failure, never the token) on every flow.

**Gate.** Unit tests: token signing/expiry, PIN expiry/rate-limit, middleware rejection. Manual: `make dev`, request PIN, see it in logs, complete login, hit authed endpoint. Document: this is not a SaaS identity system — one email, one person.

---

## Phase 5 — Live hosting & CI/CD

**Goal.** A real box on the internet with real HTTPS, reached from a laptop during development — the compatibility check at every step from here. Dockerized, so portability surprises are unlikely, but we find out early, not at the end. The same box is the CI/CD deployment target.

**Scope.**
- Provision the Netcup VPS: Ubuntu, Docker + compose, SSH. One-time setup, recorded in `docs/ops.md` (runbook), no secrets in the repo.
- DuckDNS: a subdomain (e.g., `sps.<name>.duckdns.org`, wildcard for future preview routing) pointing at the VPS; the update client runs on the host, not in the stack.
- HTTPS: a reverse-proxy sidecar (Caddy) with automatic Let's Encrypt on the DuckDNS subdomain, HTTPS-only, redirect. Phase 4's auth already gates exposure, so the box goes public only behind a login.
- `make live-check`: tries the real `https://<host>` from a laptop — `/health` 200 over TLS, an unauthenticated `/api/*` 401, then the current phase's smoke tests. Grows with every phase.
- CI/CD as a flow, not a detail: on push, `make test` → build images → deploy to the live box (SSH deploy key) → `make live-check`. First pass is one workflow/script against the deploy key; refine when we cross that bridge.
- `docs/ops.md`: box setup, deploy key rotation, DNS/TLS wiring, how the box stays current.

**Gate.** From a laptop: `curl https://<duckdns-name>/health` → 200 over TLS; an unauthenticated `/api/*` call over HTTPS → 401. `make live-check` green. One push-triggered deploy lands on the live box and passes `make live-check`.

---

## Phase 6 — Project create pipeline

**Goal.** PRD's 80% path works end-to-end over the API: create → ready in seconds. Each project gets a sandbox container from one shared image; the repo is cloned *inside* the container (the host never touches untrusted repo content, and nothing about the app needs to be dockerizable).

**Scope.**
- Shared sandbox image (embedded `Dockerfile` in `internal/project/sandbox/`: git, tmux, `ss`, SSH, CA certs on a slim base), built once locally if absent.
- `POST /api/projects` `{repoUrl?, branch?}` → run sandbox (named volumes `sps-<id>-repo` at `/workspace`, `sps-<id>-home` at `/root`, `sps-net`, writable rootfs) → `git clone` inside the container into `/workspace/repo` (blank sandbox when `repoUrl` empty) → ready. Synchronous: progress lands as event lines (`project.create/clone/ready`); clone failures land as `error` events and the sandbox stays up for manual repair.
- `GET /api/projects` (index), `GET /api/projects/{id}` (metadata + live container status via weathervane), `POST /{id}/start|stop|restart`, `DELETE /{id}?scope=container|repo|metadata|all`.
- Branch pinned when given; default to the remote's HEAD otherwise.

**Gate.** API smoke tests (`httptest` against a live server + host Docker): create from a local fixture repo (git daemon), reach "Running" with the clone present, stop it (volumes survive), restart it, delete each scope. Blank-sandbox path covered too.

---

## Phase 7 — Terminal transport

**Goal.** The heart of the product: a browser terminal that behaves like a real terminal. Sessions exist and you can watch them from a browser; Phase 8 owns what runs inside them.

**Scope.**
- Minimal sessions API (pulled up from Phase 8): `GET /api/projects/{id}/sessions` from `tmux list-sessions`; `POST .../sessions {name}` → `tmux new-session -d -s <name>` (plain shell, no harness logic). Creation is explicit; nothing auto-spawns sessions.
- `GET /ws/projects/{id}/sessions/{name}` bridging browser frames ↔ `docker exec -it tmux attach -t <name>`. Strict attach only: 404 when project, container, or session does not exist.
- Protocol: `input`, `resize` (→ `ResizeExecTTY`), `output`, `exit`. Reconnect = fresh `tmux attach`; session keeps running on disconnect.
- Minimal frontend terminal: xterm.js + `@xterm/addon-fit` in a temporary tab, resize on frame, desktop-width toggle.
- Connections emit `terminal.attach/detach` events so the log shows who was in which terminal when; explicit creates emit `session.create`.
- Creature-comfort keys as a first cut: ↑, Ctrl-C, Ctrl-L, Tab, Esc → send keystroke frame (full strip lands in Phase 11).

**Gate.** Create a session via the API, open a terminal tab, run `echo hi` and a TUI (`top`), resize the window (browser and mobile), disconnect and reconnect → same scrollback. A Go test drives the WS client against a real session.

---

## Phase 8 — Harnesses & sessions

**Goal.** "+ New Session" works: sessions are real tmux sessions, discovered not stored.

**Scope.**
- Sessions API: kill, restart; creation is harness-driven (`{harnessId}`), naming `<harness>-<n>`. Raw list + create-shell landed in Phase 7.
- Harness launch: `tmux new-session -d 'cmd || echo "[cmd exited: $?]"'` in the repo dir; expose the exit line.
- CLI validation from PRD §5: after `install`, run `command --version|--help` in the container with a forced TTY + timeout; reject non-CLI with the PRD message.
- Install-on-demand: first launch runs the plugin's `install` in the container if the `command` is missing.
- Builtins documented with runtime requirements (node for opencode/freebuff); missing runtime ⇒ visible failure with a hint.
- Add-harness form writes a plugin file (form + folder are the same thing).
- Each builtin documents where it records usage in the home volume (opencode storage dir, aider history, freebuff) — the observability page harvests these in Phase 12, the secretary reads them in Phase 13.
- Emit `session.create/exit`, `harness.launch`, `validation.failed` events.

**Gate.** Launch a fake CLI harness (fixture shell script) in a real container, list/kill/restart it, verify the `|| echo` failure path surfaces. Validation probe rejects a "not-a-CLI" fixture with the exact PRD message.

### Phase 8 completion checklist (each step verifiable on its own)

> Status: items 1–4 landed and verified (unit + both integration suites +
> race + playwright green; integration tests clean up via t.Cleanup, no
> leaked containers). Remaining: item 4's manual phone smoke against
> `go run`. Sandbox image v3 ships node/npm/python3/pip and an
> `sps-update-runtime` script for latest-stable updates on request; Vi Demo
> and Crasher Demo harnesses are seeded so the TUI and `|| echo` failure
> stories are visible from the "+ New Session" picker.

1. **Real-TUI transport gate.** The sandbox image ships busybox `vi`; use it
   as the harness command in the live integration test, drive it over the
   WebSocket bridge (keystrokes in), assert the screen changed
   (`capture-pane`). This is the faithful stand-in for coding CLIs: real PTY,
   real cursor addressing, no network installs. *Verify:* integration suite.
   DONE — sandbox image v2 adds busybox `vi`; sessions zero tmux
   `escape-time` (a lone ESC followed by a key was parsed as Meta-key and
   swallowed); command probing targets the binary (first word of `command`);
   `TestRealTUIGate` drives vi over the WS bridge and asserts the file edit.
2. **Harness config v2 — native configs, not our invention.** Plugin schema
   gains `configPath` + `config` (any JSON, the CLI's own portable format).
   At launch, before validation, the platform writes it into the container
   at exactly the path the CLI reads (`docker cp` primitive). Users paste
   their existing opencode/aider config into the settings page once; keys
   live inside it because that is how those tools take keys — no more
   invented `auth.env` indirection. *Verify:* unit tests pin the
   write-before-validate ordering; integration test launches with a config
   and asserts the file's presence + contents inside the container. DONE.
3. **Drop the `Auth` block** from the schema and builtins. Old plugin files
   containing `auth` keep parsing (unknown fields are ignored); seeds never
   overwrite user edits. *Verify:* unit tests + seed idempotence. DONE.
4. **Installed-aware "+ New Session".** `GET /api/projects/{id}/harnesses`
   (probe per container, one exec) feeds the picker so users see what is
   ready vs what will self-heal on launch. *Verify:* handler tests (done),
   playwright mock updated, manual smoke. Code + tests done; manual smoke open.
5. **Full sweep:** race detector, both integration suites, playwright, then
   a phone-browser smoke against `go run`. Automated parts green; phone
   smoke pending.

---

## Phase 9 — Preview & diff

**Goal.** `http://localhost:3000` on the box becomes a clickable URL in the browser; changed files are a list.

**Scope.**
- Port detection inside the container (`ss -tlnp`), probe (`GET /` on the detected port), per-project auto port from a pool with manual override.
- Authenticated reverse proxy (cookie in front), stream flush for SSE/WS (`FlushInterval`), no path rewriting — app served at origin root.
- Preview UI: ports panel (port, URL, state), one-tap open.
- Diff: `git status --porcelain -uno`, `git diff --numstat` → `{path, added, deleted}`, raw diff per file; "untracked: N files" note. Diff UI list + plain view.
- Emit `preview.detected` / `preview.failed` events on port probes.

**Gate.** Start `python3 -m http.server 8000` inside a project (via a button or terminal), see it in the ports panel, open the proxied URL from a phone over LAN. Make a change to the fixture repo and see it in Diff.

---

## Phase 10 — Env editor & persistence

**Goal.** Secrets are a file, masked, and survive restarts.

**Scope.**
- Env endpoints: `GET` (masked, reveal-one), `PUT` (upsert/remove) → writes `project.json` `env` (0600) and uploads `/root/.sps-env` into the container; `profile.d` snippet sources it in every login shell; new sessions see new env without a recreate.
- Secrets never in `docker inspect`, logs, or the UI; project file 0600.
- Persistence audit: repo volume, home volume, data dir — stop/start, server restart, compose down/up survival. Confirm delete scopes don't leak.
- Emit `env.changed` events with names only — never values.
- Restart behavior: sessions terminate on container restart; reopen relaunches them (state survives).

**Gate.** Set `FOO=bar`, start a session, `echo $FOO` prints bar. `docker compose down && up`, project still lists, sessions relaunch, `FOO` still set. Reveal-one masked values in UI; assert logs contain no secret.

---

## Phase 11 — Frontend completion & buttons

**Goal.** The litmus test is smooth on a phone.

**Scope.**
- Full React UI: login, projects list, project page with tabs (Sessions, Terminal, Preview, Diff, Env), "＋ New Session", empty-state "Clone a repo →".
- Action buttons: detected defaults (`package.json` scripts, Makefile, `go.mod`), custom `actions` (project file), "＋ Add" two-field form writing the file. Run in a session, stream output to terminal.
- Emit `action.run` events for every button tap (harness name, command, exit code).
- Full creature-comfort key strip (↑, Ctrl-C, Ctrl-L, Tab, Esc, ←/→), copy/paste, desktop-width mode, resize on keyboard open/rotation.
- Mobile pass: portrait/landscape, safe-areas, touch targets.

**Gate.** The PRD litmus on a real phone over LAN: open Go project → tap Run tests → tap Freebuff/conversation → close phone → come back → Diff → handed to OpenCode → Preview → push. List what broke on mobile; fix.

---

## Phase 12 — Packaging & self-hosting

**Goal.** A fresh box becomes a working instance with one command.

**Scope.**
- Server container image (multi-stage: distroless + certs; the control plane only — socket mount does the rest).
- `docker-compose.yml` (self-host): socket mount, data volume, ports `8080` + preview pool, `SMTP_*`, `LOGIN_EMAIL`.
- Quickstart doc: one command to first project.
- Observability page (PRD §12) over all three collection mechanisms: live state (containers/sessions/stats via Docker), usage aggregates from `events.log` plus best-effort harness-native records read from the home volume, server + per-container log tails, errors list, auth status, health (disk/version/uptime). Server log tail via a bounded file reader; container log tail via `docker logs`. Placed as a top-level entry on the home screen, outside any project.
- `make e2e`: runs the Phase 6–11 flow against the compose stack, verifying the "image ships images" path exactly as prod does.

**Gate.** Fresh box (or `docker run` on a laptop), `docker compose up`, create + open a project from a phone using real SMTP. Observability shows live state + a populated `events.log` + an error you deliberately caused. `make e2e` green.

---

## Phase 13 — The secretary (v1 subset)

**Goal.** PRD §11, bounded: rides authed CLI credentials, confirm-diff only, no repo writes.

**Scope.**
- System guide (the "taught bounds" doc) shipped as markdown.
- Chat bubble UI (projects screen + per project) that streams an LLM conversation using the authed CLI's provider/key (or `SPS_HELPER_API_KEY` override).
- Tools (server-side, bounded): read project.json / harness plugins / harness config in home volume / container state / **the event log** (tail + `since` filter, read-only); probe auth & ports; apply confirm-diff to the three config categories via the Phase 3 file primitive.
- v1 abilities: wire-up-my-key (masked field per harness), switch-model (one-line config diff + confirm), suggest-buttons, where-do-i-stand (portfolio briefing, backed by the event log).
- Bounds enforced in code, not prose: no repo/tmux write paths, secrets never in chat, dumb-bot (no polling/autonomy).

**Gate.** With a real API key + a fixture container, run all four v1 abilities; each edit lands only after an explicit confirm; assert the toolset cannot reach a repo path (negative test). Confirm-diff unit tests.

---

## Later, not planned now

Deferred per PRD §13: import/export (repeatable-experience substrate), harness version pins + admin update, headless-browser policy, image-tag versioning, analytics polish (graphs/export formats over today's tables + raw event log). Keep the "copy `$DATA_DIR`" backup story as the v1 story.

## Ordering rationale (waterfall gates)

1→2→3 stack the primitives before any user-facing path exists. 4 (login) gates everything exposed. 5 stands up the real box + HTTPS + CI/CD early so every step after is verified against live connectivity, not just compose. 6 gives the 80% path bare; 7 makes it interactive; 8 adds agents; 9 adds seeing+diffing; 10 hardens state; 11 is the mobile polish pass on the now-complete loop; 12 ships; 13 is the deliberately last, highest-scope feature so it cannot block the core loop.

Observability is threaded through all of it, not bolted on at 12: the log is born in 2, every phase 4–11 emits into it and exercises it in its gate, 12 merely paints the page over data that already exists, and 13 lets the secretary read the same sources for debugging.