# Devin-style preview for SPS

## Purpose

Run a real browser beside the project and stream it to the phone. The goal is
to watch and test the app while the AI edits code — not to publish the project.
This doc records the Devin/Codespaces inspiration, maps product intents to an
implementation, and surfaces every compromise.

This is a hypothesis. Devin's internals are not public.

## Current implementation foundation

Path-proxy preview removed. Project ports are not published.

Existing blocks:

- `internal/preview.Manager` — owns workers, handles concurrent ensures, closes on
  project/server shutdown. **Must change**: currently `map[projectID]Worker`
  (one per project); required is `map[projectID]map[port]Worker` for N previews
  per project.
- `docker.NetworkNamespace(containerID)` — `container:<id>` network mode.
- `internal/state.Project` — currently `StartupCommand string`; required is
  `QuickCommands map[string]string` (alias → command) with `PATCH /api/projects/:id`.
  Migrates `StartupCommand` on load.
- `POST /api/projects/:id/sessions/:name/inject {command}` — `Ctrl+C` + `command` +
  `Enter` via `tmux send-keys`. Alias resolved client-side.
- Browser runtime — Chromium + Xvfb + x11vnc + noVNC, CDP at `/json/version`,
  private `sps-net` only. **Must change**: `docker_runtime.go` hardcodes
  `BROWSER_TARGET=http://127.0.0.1:3000`; required is dynamic per selected port.

Preview flow:

```text
Preview tab (TerminalView):  polls GET /preview/slots + Refresh
  → lists live localhost ports (pills :3000, :4000 …)
  → click port N → POST /preview/start {port:N}
  → polls GET /preview/status?port=N  (starting → ready, via CDP /json/version)
  → loading → Open :N ↗

Open:
GET /preview/:project?port=N → iframe /api/projects/:id/preview/vnc.html?port=N
  → auth SPS handler → private sidecar noVNC WS → Xvfb → x11vnc → Chromium
  → Chromium at http://127.0.0.1:N (dynamic BROWSER_TARGET)
```

No host-published ports. CDP never leaves the private network.

## Short conclusion

```text
Project runs frontend+backend as locally. Browser beside project sees localhost correctly.
fetch("http://localhost:4000/api/data") works.
```

Phone shows an authenticated remote surface. Good fit for "show me what the AI is
building with live HMR"; poor fit for exact mobile Safari fidelity.

## 1. Public patterns

**Devin**: remote Chromium that renders, accepts input, exposes display + CDP.
**Codespaces**: forwards a dev port to the user browser — real browser but
`localhost` still means the phone, so API proxying is required.

SPS copies Devin's remote-browser model. Do not copy Codespaces' direct
browser-to-project as primary — it doesn't solve `localhost`.

## 2. Architecture

```text
phone → SPS HTTPS (JWT) → worker per active port → project netns → app(s)
```

No public app ports.

### 2.1 Browser worker (per port)

One worker per active localhost app. A project may have 10, another 20 — user
decides. Each worker is `BROWSER_TARGET=http://127.0.0.1:<port>`, isolated;
a crash affects only that preview. Share-nothing; optimize only after measuring.

### 2.2 `localhost` means the project

Sidecar uses `container:<project-container>` netns so loopback is shared.
No public port; SPS reaches it via private IP. Alternative (Chromium inside
project image) rejected — bloats user image.

### 2.3 Display

`Chromium → Xvfb → x11vnc → noVNC WS → canvas`. Keep on SPS origin. WebRTC later
only if VNC bandwidth warrants it.

### 2.4 Quick commands

`state.json: Project.QuickCommands map[string]string`.

- Home: project row dropdown → "Update quick-commands" → modal (alias+command rows,
  Add/Delete, Save).
- TerminalView header dropdown (Rename/Restart/Kill) also has "Update
  quick-commands" → same modal.
- Empty map allowed; alias unique non-empty.
- `PATCH /api/projects/:id {quickCommands:{...}}`.

### 2.5 Inject

```text
POST /api/projects/:id/sessions/:name/inject {command}
  → tmux send-keys -t <name> C-c
  → tmux send-keys -t <name> "<command>" Enter
```

`:name` is current tmux session ( TerminalView `current`, typically `main`). E2E
drives this via API/UI, not xterm typing.

### 2.6 Preview tab + generic port detection

TerminalView has **Terminal** and **Preview** tabs. Preview tab owns discovery.

- Backend runs `ss -tlnp || netstat` inside project container, scrapes every
  `:<port>` occurrence, de-dupes, returns `[{port,status:"live"}]`. No allow-list.
  Must handle `127.0.0.1:3000`, `0.0.0.0:3000`, `:::3000`, `*:3000`, headers,
  duplicates, empty. Strong unit tests required. **Remove** old `previewSlots`
  cap — previews are unbounded per project.
- `GET /api/projects/:id/preview/slots` — polled every 5s; Preview tab also has
  **Refresh** button.
- Empty state when none live; otherwise pills per live port. Progressive
  disclosure: only live ports shown.
- Click port → `POST /preview/start {port}` ensures worker with that
  `BROWSER_TARGET`; UI shows spinner ("Getting preview ready…") polling
  `GET /preview/status?port=N` until `ready` (CDP `/json/version` ok + display
  reachable) → **Open :N**.
- Each port has its own Open; user may have N concurrent previews. Backend-only
  ports (e.g. `:4000` JSON) still open — Chromium shows raw JSON; user can
  **Close** it (`DELETE /preview?port=N`). If port disappears, pill vanishes;
  existing preview stays until closed.

### 2.7 Browser lifetime

Lazy per-port start. Keep alive while project container lives; phone disconnect
does not stop it. Explicit `Close` per preview; `DELETE /projects/:id?scope=all`
stops all. No auto-reap until measured.

### 2.8 Viewport

Derive from display surface size; presets phone portrait/landscape/desktop.
Device emulation is a hint, not Safari emulation.

## 3. Intent map (changed rows only)

| Intent | Implementation | Cost |
|---|---|---|
| Detect any localhost server | Scrape `ss` for all ports; user picks | Backend ports also listed |
| User picks port | Preview tab + Refresh → lazy worker + Open | Extra click + loading |
| Many previews per project | One worker per port, unbounded | Memory scales with previews |
| Reuse commands | `QuickCommands` map + modal (Home+Terminal) | UI + PATCH |
| Run command quickly | `POST /inject {command}` = Ctrl+C+type+Enter | Needs session name |
| Keep app off internet | No published ports; SPS-only surface | Remote display only |
| Preserve HMR | Chromium → dev server directly | Broken HMR stays broken |

Unchanged: `localhost` sharing, root-relative paths, WebSockets, cookies,
SPS JWT for surface/WS/inject, per-project/profile isolation — see prior doc.

## 4. Interaction scope

V1: tap/click, type, scroll, common keys, select/copy/paste via remote clipboard,
link nav, back/forward, reload, dialogs, quick-commands modal + inject,
port discovery + Refresh, per-port Open/Close.

Deferred: file upload/download, drag-drop, permission prompts, multi-tab per
preview, popups, extensions, DevTools, exact Safari, full a11y.

## 5. Security

Only SPS HTTPS is public. Never publish project/VNC/noVNC/CDP/Docker socket;
WS via JWT/private network. Project is trusted user code — keep non-privileged,
no host net, minimal mounts, CPU/mem limits, per-preview isolated profiles,
CDP private. Browser profile = project data, not in logs. Outbound internet
allowed (third-party APIs); document that container network is available.

## 6. E2E strategy

Real backend, real workers, real HMR. Every `test()` includes a
`page.screenshot` / `toHaveScreenshot` of the display surface (noVNC canvas) —
the user-visible truth. CDP is for polling only (wait for HMR text), not for
screenshots.

```text
Playwright → SPS page → noVNC canvas → Chromium sidecar (netns) → app
```

### 6.1 CDP readiness

Worker's private `GET /json/version` must return `webSocketDebuggerUrl` before
`ready`. Verify: endpoint appears before ready, correct Chromium version, WS
connects, SPS reachable, public unreachable, not in logs.

### 6.2 Waiting for state

Connect only when polling: `chromium.connectOverCDP(cdp)`. Use
`Runtime.evaluate` to poll for text; gate assertions still use `page.screenshot`.

### 6.3 Network sharing

Fixture: frontend `:3000` + backend `:4000` with `fetch("http://localhost:4000/api/data")`.
Assert backend payload appears via display surface (`page.screenshot` + inspect).
Keep synthetic Vite+React fixture (`preview.helpers.ts`) — small, fast, proven HMR.

### 6.4 HMR

1. Create project via UI, add `dev` quick command, inject, wait for `:3000` pill,
   click → loading → Open → surface shows app. `toHaveScreenshot` baseline.
2. Exec-write new `src/App.jsx` via `POST /projects/exec`.
3. Poll until new marker appears.
4. `page.screenshot` after; assert before≠after with `maxDiffPixelRatio:0.10`.
No SPS reload; HMR is the fixture's Vite.

### 6.5 User+AI same browser

Change state (e.g. CDP `type`/`click` login), assert `page.screenshot` shows it,
then verify via inspect that interaction landed — catches split-brain browsers.

### 6.6 Quick-commands + inject

Add/update/delete via modal from Home and Terminal, assert persisted via
`GET /projects/:id` and `page.screenshot` of modal; inject and verify terminal
output + port appears.

### 6.7 Auth

No JWT → 401; valid JWT → surface + `page.screenshot`; cross-project isolation;
logged-out cannot open; raw CDP not public; no secrets in logs.

### 6.8 Screenshots

Only `page.screenshot` counts for user-visible assertions. `maxDiffPixelRatio:0.10`,
`animations:disabled`, fixed viewport/fonts, committed baselines
`{spec}-snapshots/*.png` (no suffix). Every test has one.

### 6.9 Full workflow

```text
create via UI → quick command → inject → port pill → loading → Open
  → backend proof + interaction + page.screenshot
  → edit file → HMR → page.screenshot diff
  → close page → reopen /preview → state persists → page.screenshot
  → desktop + phone toHaveScreenshot
```

Container and worker stay alive across disconnect.

### 6.10 Isolation

Clean container/profile/ID/session/viewport/fixture per test. No sharing across
parallel runs; `deleteAllProjects` in setup/finally. Htmx/other frameworks future.

## 7. Resources

Cost per active preview (Chromium+Xvfb+VNC+bandwidth+CDP). Expose per-preview
port/state/errors/display state/last screenshot/viewport. No auto-reap yet.

## 8. Trade-offs

Remote Chromium not native — not pixel-perfect Safari. Remote display ≠ native
selection/download/popups/a11y. State lives server-side. More moving parts
(display, input, per-port lifecycle, port scraper, quick commands, inject).
`page.screenshot` flakier than DOM checks — pin versions, relaxed threshold.
Debugging spans app/Chromium/display/VNC/WS/scraper/inject.

Does not fix broken HMR, offline backend, or non-localhost URLs.

## 9. Alternatives not chosen

Iframe (still wrong `localhost`), public ports (auth/HTTPS issues), wildcard
subdomains (gateway + localhost rewrite), SW/script rewriting, WebRTC first
(premature).

## 10. Decision gates

Prototype must answer: netns localhost works; latency acceptable; tap/type/scroll;
generic `ss` scraper; Preview tab + Refresh + lazy per-port worker; HMR; JWT
auth; disconnect/reconnect + multiple workers; full UI flow with `page.screenshot`;
memory for N previews; Safari delta acceptable; quick-commands+inject e2e.

Fixture: `:3000` Vite HMR + `:4000` backend, root-relative assets,
`fetch("http://localhost:4000/api/data")`, at least one quick command. Success =
phone creates project, adds command, injects, sees port, opens preview, uses
app, sees HMR via `page.screenshot`, reconnects.

## 11. Stages

1. Worker per port proof. 2. CDP. 3. Display. 4. Quick commands + inject.
5. Generic port scraper + Preview tab (strong unit tests). 6. Input. 7. Per-preview
lifecycle. 8. Full E2E with `page.screenshot`. 9. Resource review.

## 12. Final rule

Choose this if: need live AI-built app, `localhost` backends, remote Chromium ok,
basic interaction enough, server can afford per-preview workers, generic
port-pick UX acceptable. Skip if need native browser, exact mobile behavior,
native file/a11y, tight memory, or shareable public URL.

## References

- [Devin computer use](https://docs.devin.ai/work-with-devin/computer-use)
- [GitHub Codespaces security](https://docs.github.com/en/codespaces/reference/security-in-github-codespaces)
- [Forwarding ports in Codespaces](https://docs.github.com/en/codespaces/developing-in-a-codespace/forwarding-ports-in-your-codespace)
