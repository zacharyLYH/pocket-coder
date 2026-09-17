# Preview per-sidecar token + heartbeat presence — RFC (DRAFT, plan only)

Status: planning. No code changes yet. Decision log from discussion;
a fresh session implements from this doc without re-asking.

Locked decisions: random in-memory token (not HMAC); rotate on sidecar
lifecycle + heartbeat silence (not per start); gate surface + all tools;
golden test is full-stack e2e incl. multi-tab.

## 1. Problem

Preview URLs are predictable and long-lived: `/preview/{projectId}` and
`/api/projects/{id}/preview/...` (incl. the `websockify` WS). Auth is a
7-day `HttpOnly` `SameSite=Strict` cookie JWT — ambient (auto-attached)
and app-wide. Consequences: leaked/bookmarked URLs replay forever,
project IDs are enumerable, stale tabs silently reattach to whatever the
sidecar shows now, and the CDP tools (`screenshot`, `inspect`,
`console`, `network`, `navigate`, `click`, `type`, `reload`, `scroll`)
are live in production while **no first-party client uses them**
(web UI calls only `ports`, `start`, `close`, `tools/viewport`;
verified by grep over `web/src`).

## 2. Model

Two credentials, ANDed on preview endpoints, everywhere else unchanged:

- Cookie JWT = identity (exists today, untouched).
- Preview token = capability for one live sidecar: 128 bits from
  `crypto/rand`, hex-encoded (32 chars), per project-sidecar, stored
  only in `preview.Manager` memory, never on disk, never logged.

Invariant: **rotation happens iff no live token holder exists.**
Holders prove liveness two ways: an open `/preview` page beating, or
any valid-token API call. Silence past threshold ⇒ nobody to disrupt.

## 3. Server changes (`server/`)

### 3.1 `internal/preview/worker.go` — state + sweeper

```go
// alongside workers/inflight, under the same mu:
tokens   map[string]string
lastSeen map[string]time.Time
sweepStop chan struct{}
sweepOnce sync.Once
```

- `Ensure`: after `factory.Start` succeeds, `tokens[id] = newToken()`
  (16 bytes `crypto/rand`, hex) and `lastSeen[id] = now`. Existing
  worker returned untouched (no rotation on re-Ensure — port `start`s
  must not disturb sibling tabs).
- `Token(id) (string, bool)` / `CheckToken(id, sup) bool`
  (`subtle.ConstantTimeCompare`).
- `Touch(id)` — set `lastSeen[id] = now`, called by every request
  presenting a **valid** token (heartbeat *and* tools; invalid tokens
  never touch). This is what keeps long API-driven specs alive without
  an open page, and why `ports` (ungated, pre-sidecar discovery) must
  stay token-free *and* touch-free.
- `Sweep(now time.Time)` — delete `tokens[id]` + `lastSeen[id]` where
  `now.Sub(lastSeen) > silence`. Pure function of `now`: unit-tested
  without clocks.
- `StartSweeper(interval, silence)` — one global ticker, `sync.Once`
  guarded; `Close` stops it first (extends existing idempotent `Close`).
- `Stop` additionally deletes token + lastSeen (explicit close =
  immediate death, no waiting for silence).

### 3.2 Env knobs (no `config` package change)

`main.go` reads and passes to `StartSweeper` / `NewManager`:

- `PCODER_PREVIEW_SWEEP_INTERVAL` (default `30s`)
- `PCODER_PREVIEW_TOKEN_SILENCE` (default `3m`; invariant: silence >
  2× heartbeat interval in every env)

`docker-compose.e2e.yml` server env adds
`${PCODER_PREVIEW_TOKEN_SILENCE:-3m}` /
`${PCODER_PREVIEW_SWEEP_INTERVAL:-30s}` passthrough so e2e can
stipulate short lifetimes without touching dev/prod.

### 3.3 `internal/httpapi/preview.go` — mint, return, gate

- `GET /preview` status and `POST /preview/start` responses gain
  `"token"` (absent when stopped). These two stay cookie-only: they
  are the bootstrap that *issues* the capability.
- `DELETE /preview` stays cookie-only (an expired-token holder must
  still be able to close).
- `handlePreviewSurface`: require `?token=` via `CheckToken`; miss →
  `404 {"error":"not found"}` — byte-identical shape to
  preview-not-running (no oracle). `newPreviewProxy`'s `Director`
  **strips** `token` before forwarding upstream (today arbitrary query
  passes through — see `preview_proxy_test.go:17`, which must be
  updated to assert stripping).
- New `POST /preview/heartbeat`: check token → `Touch` → `200 {ok}`.
  Miss → 404 (client treats as expired, stops beating).

### 3.4 `internal/httpapi/preview_tools.go` — one helper

```go
// checkPreviewToken validates X-Preview-Token (else ?token=),
// Touches lastSeen on success, writes 404 on failure.
func checkPreviewToken(d Deps, w http.ResponseWriter, r *http.Request) bool
```

Called at the top of: screenshot, inspect, console, network,
navigate, click, type, reload, scroll, viewport. `ports` explicitly
excluded (see §3.1).

## 4. Client changes (`web/`)

- `lib/preview.ts`: `previewSurfacePath(projectId, token)` appends
  `token=` to the page URL **and** embeds it in the `websockify`
  `path` value. `HEARTBEAT_INTERVAL_MS` exported const, default
  `30000`, overridable via `VITE_PREVIEW_HEARTBEAT_MS` (e2e sets
  `5000` in the playwright `webServer` vite command).
- `PreviewSurface`: owns the token lifecycle, no routing change
  (`/preview/:id` stays). Mount → `GET status` (no token yet →
  "not running, start from Preview tab" if stopped) → iframe with
  token src → heartbeat loop (mount + interval + `visibilitychange →
  visible`, mirroring the viewport pattern) → heartbeat 404 →
  expired card ("preview expired") with one-click Resume
  (re-`status` → fresh token → resume beating + reload iframe).
- `PreviewTab`: unchanged (ports/start/close are bootstrap, no token
  needed). `onOpenPreview` unchanged.

## 5. Rotation semantics (the multi-tab answer)

No refresh-while-siblings-exist: rotation events are sidecar
replacement, explicit `Stop`/delete, server restart, and sweeper
expiry after total silence. Tabs sharing a sidecar share one token;
frontend `:5173` + backend `:8000` tabs coexist with zero extra code —
a timestamp, not a counter (crash-safe: dead tabs simply stop
beating). The only tab that ever sees a dead token slept past
threshold; Resume makes that a one-click, fail-closed path.

## 6. Golden e2e (`web/e2e/preview.token.spec.ts`)

Env (stipulated): `PCODER_PREVIEW_TOKEN_SILENCE=20s`,
`PCODER_PREVIEW_SWEEP_INTERVAL=5s`, `VITE_PREVIEW_HEARTBEAT_MS=5000`
(20s > 2×5s: one missed beat tolerated, total silence rotates).
Helpers (extend `preview.helpers.ts`): `statusToken(request, id)`,
`surfaceToken(page)` (parse `token=` from the iframe `src`),
`openSurfacePage(browser, id)` (new tab on `/preview/{id}`, wait for
iframe src containing `token=`).

1. **Single-tab rotation.** Open surface tab A → record token T1 →
   close A → wait silence+sweep margin (~30s) → old surface URL with
   T1 → expect 404; `statusToken` → T2 ≠ T1. Reopen (goto
   `/preview/{id}`) → iframe token == T2. Budget ≈ 60s.
2. **Multi-tab persistence.** Open A + B (same project) → T1 both →
   close A → wait past silence threshold → `statusToken` still T1,
   B heartbeat 200, B surface 200 with T1 (at least one tab open ⇒
   alive). Close B → wait → rotated (T2 ≠ T1). Budget ≈ 2.5 min.
3. **Tools gating (fast, no waiting).** Valid `X-Preview-Token` →
   200; wrong/missing → 404 shaped exactly like not-running;
   `ports` without token → 200 (bootstrap invariant).

Flake guards: generous margins (assert at silence+2×sweep), headless
tabs count as visible for timers, `finally { deleteAllProjects }`
per repo convention, `test.setTimeout(600_000)` like
`preview.auth.spec.ts`.

## 7. Unit tests

- Go (`internal/preview/`): mint-on-create, no-rotate-on-reEnsure,
  `Touch` refresh, `Sweep` expiry boundary (param-injected `now`),
  `Stop`/sweeper deletion, restart-forgets (fresh Manager).
- Go (`internal/httpapi/`): surface/tools 404-without-token,
  404-shape-equals-not-running, token stripped by proxy Director
  (update `preview_proxy_test.go`), heartbeat touch + 404, ports
  ungated, start/status return token, close works tokenless.
- Web: `previewSurfacePath` token embedding, heartbeat loop start/
  stop on mount/unmount, 404 → expired UI → Resume refetch
  (update `PreviewSurface.test.tsx` exact-src assertion).

## 8. Existing tests to touch

`preview_proxy_test.go` (stripping), `PreviewSurface.test.tsx`
(src), every e2e spec calling tools without a token (add
`X-Preview-Token` via a shared helper — `preview.tools`,
`preview.basic`, `preview.hmr`, `preview.vue`, `preview.vanilla`,
`preview.reconnect`, `preview.auth` §28), plus any spec doing
API-only long sessions (their valid-token calls now sustain presence
via `Touch` — no heartbeat needed).

## 9. Security properties (condensed)

Stops: URL replay/leak/enumeration, stale-tab reattach, same-site
link/CSRF driving another project's browser, unauthenticated use
after close, and any auth-middleware bypass (handler-level check is
behind the bypass). Does NOT stop: JWT theft (holder mints own
token), own-origin XSS (token is JS-readable by design), sidecar
breakout/container escape, passive log exposure of query tokens
(headers used wherever possible; rotation bounds the rest).

## 10. Rollout

Same-ship frontend + backend (one compose): pre-token clients get
404s on surface/tools after server upgrade, so no staged rollout.
No migration (memory-only), no `state.json` change, no new
infrastructure. Follow-up, not in scope: idle-TTL tuning from prod
data, access-log query scrubbing.
