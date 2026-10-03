# Preview renders blank: noVNC assets 404 on the preview token gate

Findings from iterating on the macOS preview failures with the smallest
preview e2e (`preview.vanilla.spec.ts`, one test). Two things came out of it:

1. The relay bridge-IP fix (see `preview-relay-mac-loopback.md`) **works** —
   the containerized server now reaches the relay. Verified below.
2. The preview surface still never renders, but **the relay is no longer the
   cause**. The blocker is a separate, platform-independent bug: the preview
   token gate 404s noVNC's module assets because relative module imports can't
   carry the `?token=` query.

TL;DR: relay = fixed. Surface = fixed too — the gate now hands the
capability to a cookie the relative imports inherit ("Resolution" below),
and `preview.vanilla.spec.ts` is green.

---

## Topology (recap, so the failure reads correctly)

Four network namespaces across three hosts:

- **Server** — containerized (`docker compose up --build server`), on
  `pcoder-net`.
- **Project** — a container per project, on `pcoder-net`. Dev servers bind
  `127.0.0.1` inside it.
- **Sidecar** — the preview Chromium, `Network: container:<project>`, so it
  shares the project netns and reaches localhost-bound dev servers. Its CDP is
  a socat forward on `:9223`; its noVNC/websockify is on `:6080`.
- **Relay** — Docker-Desktop-only fallback, its own container on `pcoder-net`,
  socat-forwarding sidecar `:9223`/`:6080`.

On Docker Desktop, bridge IPs are not dialable from the Mac host, and the
server's `127.0.0.1` is its own loopback — hence the relay. Container→container
over `pcoder-net` **is** routable on Desktop, which is what the bridge-IP fix
exploits.

---

## Part 1 — Relay fix: verified working

### The evidence (from a live run)

`server/internal/preview/docker_runtime.go` `startRelay` now returns
`http://<relay-bridge-IP>:9223` / `:6080`. In the server log of a real run:

```
preview phase: ensure image         duration=12ms
preview phase: run sidecar          container=bd5378159d94
preview phase: inspect containers   duration=10ms
preview phase: direct CDP probe     reachable=false          ← sidecar not up in ~200ms
preview phase: start relay          cdp=http://172.21.0.6:9223   ← relay bridge IP, NOT 127.0.0.1
preview phase: wait for CDP         duration=415ms   endpoint=http://172.21.0.6:9223
preview started                     total_duration=1.218s relay=true
```

Note `reachable=false` on the direct probe: on this Mac the probe only tries
~5× quickly, and Chromium isn't listening on the sidecar's `:9223` yet, so
`Start` always falls back to the relay here. That is expected and fine — the
relay endpoint is the one that matters, and it answers.

### Independent proof the container reaches the relay

Run the preview, then probe from **inside the server container** (mid-run, the
relay is alive):

```sh
RUN=vanilla-check
API=8190
cd web
env E2E_RUN_ID=$RUN E2E_API_PORT=$API E2E_WEB_PORT=5280 E2E_GIT_PORT=9180 \
  npx playwright test --config=playwright.config.ts --reporter=list e2e/preview.vanilla.spec.ts &

# once the relay container exists:
RELAY=$(docker ps -q --filter name=pcoder-preview-relay-e2e-$RUN)
IP=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$RELAY")
docker exec pcoder-e2e-${RUN}-server-1 wget -q -O- "http://$IP:9223/json/version" | head -3
docker exec pcoder-e2e-${RUN}-server-1 wget -q -O- "http://$IP:6080/vnc_lite.html" | head -3
```

Observed: `:9223/json/version` returns real Chrome version JSON, `:6080`
returns the noVNC `vnc_lite.html` HTML. So TCP through the relay works for both
CDP and the display.

### The host-side asymmetry (why loopback couldn't work)

Same run, from the **Mac host**:

```sh
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:<published-novnc>   # 200  (loopback pub)
curl -s -o /dev/null -w '%{http_code}\n' http://<relay-bridge-IP>:6080        # 000  (unreachable)
```

`docker inspect` on the relay shows the publications: `6080/tcp -> 127.0.0.1:54885`,
`9223/tcp -> 127.0.0.1:54886`.

So: **containerized server → bridge IP works, loopback doesn't. Host → loopback
works, bridge IP doesn't.** The old `http://127.0.0.1:<pub>` endpoint was the
server talking to itself — exactly the bug in
`preview-relay-mac-loopback.md`.

### Why the e2e still fails then (jump to Part 2)

With the relay correct, the preview **starts** (`preview started relay=true`)
and the surface page **opens** (`Preview opened` is logged). The canvas still
never appears — for a different reason.

the e2e still fails, and the culprit is NOT the relay — it's the preview token gate. The browser never renders the noVNC canvas, and the reason is:
browser GET /api/projects/e2e%2F…/preview/core/rfb.js → 404 {"error":"not found"}
same URL + ?token=<valid> → 200 (noVNC rfb.js source)
noVNC's vnc_lite.html does import RFB from './core/rfb.js' — a relative module import cannot inherit the ?token= query from the page URL, so every asset hits handlePreviewSurface's gate and 404s. This is platform-independent (browser spec), introduced by af56fbb "preview token" (Sep 18); the committed preview baselines predate it (Sep 1/4). The relay is irrelevant to this failure — assets proxy fine (200) once the token is present.

## Part 2 — Surface blocker: token gate 404s noVNC's module assets\n\n### Symptom\n\n`preview.vanilla` (and any spec that waits for the canvas) fails at\n`preview.helpers.ts:271`:\n\n`\nError: expect(locator).toBeVisible() failed\nLocator: iframe[title=\"Remote project preview\"] >> canvas\nTimeout: 60000ms ... element(s) not found\n`\n\nThe iframe's body is stuck at `Loading` (plus the `Send CtrlAltDel` button).\n\n### Evidence\n\nCaptured from the browser (console + failed requests) while the surface is open:\n\n`\nresp404:    http://localhost:<WEB_PORT>/api/projects/e2e%2Fdiag4-1/preview/core/rfb.js\nreqfailed:  .../preview/core/rfb.js :: net::ERR_ABORTED\n`\n\nAnd probed directly with the session cookie (`request` in the diag spec):\n\n`\nasset-no-token   = 404  \"{\\\"error\\\":\\\"not found\\\"}\\n\"\nasset-with-token = 200  \"/*\\n * noVNC: HTML5 VNC client\\n * ...\"\n`\n\nSo the relay proxies the asset fine — the server refuses it without the token.\n\n### Root cause\n\n`handlePreviewSurface` (`server/internal/httpapi/preview.go`) gates **every**\nsurface path on the token in the **query**:\n\n`go\nif !d.Preview.CheckToken(id, r.URL.Query().Get(\"token\")) {\n    writeErr(w, http.StatusNotFound, \"not found\")\n    return\n}\n`\n\nThe entry document is loaded with the token (`previewSurfacePath` in\n`web/src/lib/preview.ts` builds `.../preview/vnc_lite.html?...&token=...`). But\nthat document then does a **relative module import**:\n\n`html\n<script type=\"module\" crossorigin=\"anonymous\">\n    import RFB from './core/rfb.js';   // /usr/share/novnc/vnc_lite.html\n</script>\n`\n\nA relative import resolves against the document URL and **drops the query**\n(`.../preview/vnc_lite.html?token=X` → `.../preview/core/rfb.js`, no token).\nThat request hits the gate with no token → 404 → `rfb.js` never loads → noVNC\nnever constructs its canvas. Every transitive import (`core/util.js`,\n`vendor/*`, …) fails the same way.\n\nIntroduced by `af56fbb \"preview token\"` (2026-09-18). The committed preview\nbaselines predate it (`preview.vanilla.spec.ts-snapshots/preview-vanilla-initial.png`,\n2026-09-04, shows a fully rendered preview), which is consistent with the\nsurface breaking at the token change.\n\n### How to reproduce (browser + direct probe)\n\nDrop this throwaway spec in `web/e2e/` and run it (it does not touch the\nchecked-in snapshots; delete it afterwards):\n\n``ts\n// web/e2e/zzdiag.spec.ts\nimport { expect, test } from './test'\nimport { deleteAllProjects, engineUp, projectURL } from './helpers'\nimport { createVanillaProject, statusToken } from './preview.helpers'\n\ntest('diag surface websocket', async ({ page, request }) => {\n  test.setTimeout(300_000)\n  test.skip(!(await engineUp(request)), 'no engine')\n  await deleteAllProjects(request)\n  try {\n    const projectID = await createVanillaProject(request)\n    const started = await request.post(`/api/projects/${projectURL(projectID)}/preview/start`, { data: { port: 3000 } })\n    console.log('DIAG start=', started.status(), (await started.text()).slice(0, 300))\n    const token = await statusToken(request, projectID)\n    const enc = encodeURIComponent(projectID)\n    const noTok = await request.get(`/api/projects/${enc}/preview/core/rfb.js`)\n    console.log('DIAG asset-no-token=', noTok.status(), JSON.stringify((await noTok.text()).slice(0, 120)))\n    const withTok = await request.get(`/api/projects/${enc}/preview/core/rfb.js?token=${token}`)\n    console.log('DIAG asset-with-token=', withTok.status(), JSON.stringify((await withTok.text()).slice(0, 120)))\n\n    const logs: string[] = []\n    const surface = await page.context().newPage()\n    surface.on('console', (m) => logs.push(`console.${m.type()}: ${m.text()}`))\n    surface.on('requestfailed', (r) => logs.push(`reqfailed: ${r.url()} :: ${r.failure()?.errorText}`))\n    await surface.goto(`/preview/${encodeURIComponent(projectID)}`)\n    surface.on('response', (r) => { if (r.status() >= 400) logs.push(`resp${r.status()}: ${r.url()}`) })\n    const frame = surface.locator('iframe[title=\"Remote project preview\"]')\n    await expect(frame).toBeVisible({ timeout: 60_000 })\n    await surface.waitForTimeout(25_000)\n    const cframe = frame.contentFrame()\n    console.log('DIAG canvases=', await cframe.locator('canvas').count())\n    console.log('DIAG bodytext=', (await cframe.locator('body').innerText().catch((e) => `ERR ${e}`)).slice(0, 800))\n    console.log('DIAG logs=', JSON.stringify(logs, null, 1))\n  } finally {\n    await deleteAllProjects(request)\n  }\n})\n``\n\n`sh\ncd web\nenv E2E_RUN_ID=diag E2E_API_PORT=8210 E2E_WEB_PORT=5300 E2E_GIT_PORT=9200 \\\n  npx playwright test --config=playwright.config.ts --reporter=list e2e/zzdiag.spec.ts\n`\n\nExpected output:\n\n`\nDIAG asset-no-token= 404 \"{\\\"error\\\":\\\"not found\\\"}\\n\"\nDIAG asset-with-token= 200 \"/*\\n * noVNC: HTML5 VNC client\\n * ...\"\nDIAG canvases= 0\nDIAG bodytext= Loading\nSend CtrlAltDel\nDIAG logs= [ ..., \"resp404: .../preview/core/rfb.js\", \"reqfailed: .../preview/core/rfb.js :: net::ERR_ABORTED\" ]\n`\n\n### Ruled out (so these aren't chased again)\n\n- **Go routing** is not the problem. A `ServeMux` with\n `GET /api/projects/{id}/preview/{path...}` matches both the entry doc and\n nested assets even when the id is `e2e%2Ffoo` (encoded slash): a minimal\n repro returns `200 id=\"e2e/foo\" path=\"core/rfb.js\"`.\n- **The upstream (websockify) serves the asset.** Running the browser image's\n `websockify --web=/usr/share/novnc 6080 …` and fetching `/core/rfb.js`\n returns `200 WebSockify`.\n- **The relay** — `asset-with-token=200` proves the relay path carries the\n asset correctly. The 404 is the server's own token gate.\n- **The token itself is valid** — the same request with `?token=` succeeds.\n\n### Why \"Linux CI is green\" needs re-checking\n\nModule import resolution is spec-defined and identical on Chromium/Chrome, so\nthis should fail on Linux CI too. The specs that wait for the canvas\n(`preview.vanilla`, `preview.basic`, `preview.journey`, `preview.reconnect`,\n`preview.port`) would fail the same way; the baselines for those specs are older\nthan `af56fbb`. Worth confirming CI status before treating the relay fix as the\nlast blocker.\n\n---\n\n## Side finding — host-side integration test regression\n\n`server/internal/preview/integration_test.go`\n`TestDockerFactoryStartsReachableWorker` runs the factory from the **host** and\nasserts the returned endpoint answers CDP from the host. With the endpoint now\nthe relay **bridge IP**, that no longer holds on Docker Desktop (host → bridge\nIP = `000`). The endpoint's reachability is position-dependent:\n\n| consumer | works with |\n|---|---|\n| server process in a container | relay bridge IP (current) |\n| server process on the host | relay loopback publication `127.0.0.1:<pub>` |\n\nThe loopback publications are kept \"for host-side debugging\" but are no longer\nwhat the factory returns, so a host-run server (and this integration test) can't\nreach the returned endpoint on Desktop.\n\n---\n\n---

## Resolution (2026-10-03) — option 1, the capability cookie

The "open decision" above is decided: option 1 is implemented. The two
rejected options are kept under "Options considered" at the bottom.

**Server — `handlePreviewSurface` (`server/internal/httpapi/preview.go`)**

- The gate reads the token from `?token=` and, only when the query carries
  none, from a per-project cookie; either must pass `CheckToken`.
- An explicit `?token=` stays authoritative: a wrong or rotated one still
  404s **even when a live cookie is present**, so the fallback can never
  paper over a stale token (`preview.token.spec.ts`'s stale-surface 404
  keeps its meaning).
- Serving the entry document (`vnc.html` / `vnc_lite.html`) with a valid
  query token hands the same capability to a cookie that the asset requests
  inherit automatically (same origin):
  - name `pcoder_preview_<url.QueryEscape(id)>` — per project, so two open
    surfaces never clobber each other, and cookie-name-safe for ids like
    `e2e/foo`;
  - `Path=/api/projects` — browser requests carry the id percent-encoded
    (`e2e%2Ffoo`) while `r.PathValue("id")` is decoded, so a per-project
    cookie path would never match an id containing a slash;
  - `HttpOnly`, `SameSite=Lax`, session lifetime (no stale capability
    outliving the browser session).
- Unchanged: `websockify` upgrades still carry `?token=` inside the path
  value, and the reverse proxy still strips `token` before the sidecar.

Why this option: the gate still covers **every** surface path (entry doc,
every transitive module asset, `websockify`) — option 3 would have opened
non-entry paths to the session cookie alone, option 2 rewrites the URL
contract plus the frontend and several specs — and no frontend code or spec
URL changed at all.

**Test:** `TestPreviewSurfaceCookieCarriesAssetImports`
(`server/internal/httpapi/preview_proxy_test.go`) pins the flow end to end:
tokenless asset 404 → entry doc with `?token=` 200 *and* the cookie set →
asset with only the cookie 200 → asset with a wrong `?token=` plus the live
cookie 404. `go build ./...`, `go vet ./internal/...` and
`go test ./internal/httpapi/ ./internal/preview/` are green.

### Verification: `preview.vanilla.spec.ts` (the one test in this doc's scope)

```sh
cd web
env E2E_RUN_ID=vfix E2E_API_PORT=8190 E2E_WEB_PORT=5280 E2E_GIT_PORT=9180 \
  npx playwright test --config=playwright.config.ts --reporter=list e2e/preview.vanilla.spec.ts
```

Before: failed at `preview.helpers.ts:271` — `iframe >> canvas` never
appeared, body stuck at `Loading` (the symptom this doc exists for).
After: the canvas appears and the run reaches the screenshot assertion.

---

## Found while verifying: the popup baseline predates `7ae0027`

The first post-fix run failed one step later, at the screenshot:

```
93450 pixels (ratio 0.11 of all image pixels) are different.
> 22 | await expect(previewPage).toHaveScreenshot('preview-vanilla-initial.png', ...)
```

Decoding both PNGs shows the actual shot equals the baseline under a **pure
45px vertical translation** — mean abs diff `0.21` at `dy=45` vs `17.4` at
`dy=0`, `dx=0`, no scaling (so it is not a fit/resize difference). The
baseline simply has 45px more content at the top: the `<header>` (Back /
Preview) that `7ae0027 "delete preview header"` (2026-09-12) removed —
`p-3` (12+12) + a `text-sm` line (20) + `border-b` (1) = 45px. The PNG dates
from 2026-09-04, so it was never re-shot after the header went away; since
2026-09-18 the token gate made these specs die earlier (no canvas), which
hid the staleness behind the bigger failure.

Fix: re-shot with `--update-snapshots=changed`, then re-ran **without** the
flag: `2 passed (34.8s)` — the new baseline holds on a clean run. The popup
page is chromeless now, so every pixel in the shot comes from the sidecar's
containerized Chromium and the PNG stays platform-independent.

Still stale, out of scope here (one vanilla test per iteration): the other
popup baselines — `preview.basic`, `preview.fit`, `preview.hmr`,
`preview.htmx`, `preview.journey`, `preview.reconnect`, `preview.vue` — are
dated 2026-09-01/04 and carry the same 45px header, so they will fail at the
screenshot step for the same reason and need the same
`--update-snapshots=changed` pass.

---

## Options considered (for the record)

The surface must load noVNC's assets, which can't carry a query token:

1. **Cookie** — set a preview cookie when the entry doc loads; the gate
   accepts the token from the query or the cookie. No URL/contract change.
   **← implemented, see "Resolution" above.**
2. **Path token** — put the token in the surface URL path so relative
   imports inherit it (`.../preview/<token>/vnc_lite.html`). Uniform gating;
   touches the frontend, handler, and several specs.
3. **Exempt static assets** — token gates only the entry doc +
   `websockify`; other assets need just the session cookie. ~3 lines; keeps
   current tests, but drops the gate off non-entry surface paths.

