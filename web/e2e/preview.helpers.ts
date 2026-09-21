import { expect, type APIRequestContext, type Page } from '@playwright/test'
import { createProject, e2eRepo, projectURL } from './helpers'

// Real full-stack fixture per devin-clone.md section 6.3:
//   frontend: localhost:3000, backend: localhost:4000
//   fetch("http://localhost:4000/api/data") — DIRECT, not through proxy.
// This proves Chromium shares the project's network namespace.`  
// Also includes login form, counter, and HMR (section 6.4).
export const reactFiles: Record<string, string> = {
  'package.json': JSON.stringify({
    private: true,
    scripts: { dev: 'vite' },
    dependencies: { react: '18.3.1', 'react-dom': '18.3.1', vite: '5.4.14' },
  }),
  'index.html': '<div id="root"></div><script type="module" src="/src/main.jsx"></script>',
  'src/main.jsx':
    "import React from 'react'; import { createRoot } from 'react-dom/client'; import { App } from './App.jsx'; createRoot(document.getElementById('root')).render(<App />);",
  'src/App.jsx': [
    "import React, { useState, useEffect } from 'react'",
    '',
    'export function App() {',
    '  const [user, setUser] = useState(null)',
    '  const [name, setName] = useState("")',
    '  const [count, setCount] = useState(0)',
    '  // Section 6.3: DIRECT fetch to backend — proves network namespace sharing',
    '  const [backendData, setBackendData] = useState(null)',
    '',
    '  useEffect(() => {',
    '    fetch("http://localhost:4000/api/data")',
    '      .then(r => r.json())',
    '      .then(setBackendData)',
    '      .catch(() => setBackendData({error:"fetch failed"}))',
    '  }, [])',
    '',
    '  const login = () => {',
    '    fetch("http://localhost:4000/api/user", {',
    '      method: "POST",',
    '      headers: {"Content-Type":"application/json"},',
    '      body: JSON.stringify({name})',
    '    }).then(r=>r.json()).then(setUser)',
    '  }',
    '',
    '  const logout = () => { setUser(null); setName("") }',
    '',
    '  const increment = () => {',
    '    fetch("http://localhost:4000/api/counter", {',
    '      method: "POST",',
    '      headers: {"Content-Type":"application/json"},',
    '      body: JSON.stringify({action:"increment"})',
    '    }).then(r=>r.json()).then(d => setCount(d.count))',
    '  }',
    '',
    '  const reset = () => {',
    '    fetch("http://localhost:4000/api/counter", {',
    '      method: "POST",',
    '      headers: {"Content-Type":"application/json"},',
    '      body: JSON.stringify({action:"reset"})',
    '    }).then(r=>r.json()).then(d => setCount(d.count))',
    '  }',
    '',
    '  if (!user) return (',
    '    <main data-testid="login-screen" style={{fontFamily:"sans-serif",padding:32}}>',
    '      <h1>Full-Stack App</h1>',
    '      <p data-testid="backend-data">{backendData ? JSON.stringify(backendData) : "loading..."}</p>',
    '      <form onSubmit={e => { e.preventDefault(); login() }}>',
    '        <input data-testid="name-input" value={name} onChange={e=>setName(e.target.value)} placeholder="Your name" />',
    '        <button data-testid="login-btn" type="submit">Log in</button>',
    '      </form>',
    '    </main>',
    '  )',
    '',
    '  return (',
    '    <main data-testid="app-screen" style={{fontFamily:"sans-serif",padding:32}}>',
    '      <h1>Hello, <span data-testid="user-name">{user.name}</span>!</h1>',
    '      <p data-testid="backend-data">{backendData ? JSON.stringify(backendData) : "loading..."}</p>',
    '      <p data-testid="counter-value">Count: {count}</p>',
    '      <button data-testid="increment-btn" onClick={increment}>+1</button>',
    '      <button data-testid="reset-btn" onClick={reset}>Reset</button>',
    '      <button data-testid="logout-btn" onClick={logout}>Log out</button>',
    '    </main>',
    '  )',
    '}',
  ].join('\n'),
  // Backend on port 4000 — no npm dependencies needed.
  'server.js': [
    'const http = require("http")',
    'let counter = 0',
    'const srv = http.createServer((req, res) => {',
    '  res.setHeader("Access-Control-Allow-Origin", "*")',
    '  res.setHeader("Access-Control-Allow-Methods", "GET,POST,OPTIONS")',
    '  res.setHeader("Access-Control-Allow-Headers", "Content-Type")',
    '  if (req.method === "OPTIONS") { res.writeHead(204); res.end(); return }',
    '  const json = (code, data) => {',
    '    res.writeHead(code, {"Content-Type":"application/json"})',
    '    res.end(JSON.stringify(data))',
    '  }',
    '  if (req.url === "/api/data") {',
    '    json(200, {source:"backend", ts: Date.now()})',
    '  } else if (req.url === "/api/user" && req.method === "POST") {',
    '    let body = ""',
    '    req.on("data", c => body += c)',
    '    req.on("end", () => {',
    '      try { const {name} = JSON.parse(body); json(200, {name, ts: Date.now()}) }',
    '      catch { json(400, {error:"bad json"}) }',
    '    })',
    '  } else if (req.url === "/api/counter" && req.method === "GET") {',
    '    json(200, {count: counter})',
    '  } else if (req.url === "/api/counter" && req.method === "POST") {',
    '    let body = ""',
    '    req.on("data", c => body += c)',
    '    req.on("end", () => {',
    '      try {',
    '        const {action} = JSON.parse(body)',
    '        if (action === "increment") counter++',
    '        else if (action === "reset") counter = 0',
    '        json(200, {count: counter})',
    '      } catch { json(400, {error:"bad json"}) }',
    '    })',
    '  } else { json(404, {error:"not found"}) }',
    '})',
    'srv.listen(4000, () => console.log("backend:4000"))',
  ].join('\n'),
}

// ── shared project-creation plumbing ──────────────────────────────────
// createProject comes from helpers.ts (with 409-retry cleanup).
// writeFilesCmd writes file contents into /workspace/app. The running-Vite
// and preinstalled flows below reuse these, so each framework fixture is
// just its file map plus a readiness variant.
function writeFilesCmd(files: Record<string, string>): string {
  return Object.entries(files).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
}

async function waitForNpmInstall(request: APIRequestContext, id: string) {
  for (let i = 0; i < 60; i++) {
    const s = await execInProject(request, id, 'cat /tmp/pcoder-npm.status 2>/dev/null || echo pending')
    if (s.trim() === '0') return
    if (s.trim() === '1') throw new Error('npm install failed')
    await new Promise((r) => setTimeout(r, 2000))
  }
  throw new Error(`npm install in project ${id} timed out`)
}

// Vite dev server on :3000 plus the shared Node backend on :4000, started
// immediately and held until both answer. Used by the React/Htmx/Vue fixtures.
async function createRunningViteProject(request: APIRequestContext, files: Record<string, string>, slot = 1): Promise<string> {
  return createRunningViteProjectOnPort(request, 3000, files, slot)
}

// createRunningViteProjectOnPort is the parameterized version: spins up the
// same Vite + Node backend fixture on an arbitrary port, so e2e tests can
// exercise BROWSER_TARGET paths other than the historical :3000 default.
export async function createRunningViteProjectOnPort(request: APIRequestContext, port: number, files: Record<string, string>, slot = 1): Promise<string> {
  const id = await createProject(request, e2eRepo(slot))
  const command = `${writeFilesCmd(files)}; nohup bash -lc '
    cd /workspace/app;
    npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/pcoder-npm.log 2>&1;
    echo $? >/tmp/pcoder-npm.status;
    node server.js >/tmp/pcoder-backend.log 2>&1 &
    exec npm run dev -- --host 0.0.0.0 --port ${port} >/tmp/pcoder-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  await waitForProjectAppOnPort(request, id, port)
  return id
}

// Preinstalled project with code but no running servers, plus shortcuts
// so a test can start them via inject (the user-driven flow).
async function createPreinstalledProject(request: APIRequestContext, files: Record<string, string>, slot = 1): Promise<string> {
  const id = await createProject(request, e2eRepo(slot))
  const install = `${writeFilesCmd(files)}; cd /workspace/app && npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/pcoder-npm.log 2>&1; echo $? >/tmp/pcoder-npm.status`
  const res = await request.post('/api/projects/exec', { data: { projectIds: [id], command: install } })
  expect(res.ok()).toBeTruthy()
  await waitForNpmInstall(request, id)
  await request.patch(`/api/projects/${projectURL(id)}`, {
    data: { shortcuts: [
      { id: 's-dev', alias: 'dev', kind: 'cmd', command: 'cd /workspace/app && npm run dev -- --host 0.0.0.0 --port 3000' },
      { id: 's-backend', alias: 'backend', kind: 'cmd', command: 'cd /workspace/app && nohup node server.js >/tmp/pcoder-backend.log 2>&1 &' },
    ] },
  })
  return id
}

// React fixture: full-stack app (frontend 3000, backend 4000, counter, HMR).
export async function createReactProject(request: APIRequestContext, slot = 1): Promise<string> {
  return createRunningViteProject(request, reactFiles, slot)
}

// Precreated React project with code but no running servers.
export async function createPrecreatedProject(request: APIRequestContext, slot = 1): Promise<string> {
  return createPreinstalledProject(request, reactFiles, slot)
}

export async function execInProject(request: APIRequestContext, id: string, command: string): Promise<string> {
  const response = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as { results: { status: string; detail?: string }[] }
  expect(body.results[0]?.status).toBe('ok')
  return body.results[0]?.detail ?? ''
}

// waitForInspectContaining polls CDP inspect until the page HTML contains
// the marker (e.g. HMR applied). Shared by every spec that edits container
// files and waits for the sidecar to pick it up.
export async function waitForInspectContaining(
  request: APIRequestContext,
  projectID: string,
  marker: string,
  attempts = 60,
): Promise<void> {
  const token = await statusToken(request, projectID)
  for (let i = 0; i < attempts; i++) {
    const res = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
    if (res.ok() && ((await res.json()) as { html: string }).html.includes(marker)) return
    await new Promise((r) => setTimeout(r, 1000))
  }
  throw new Error(`inspect for ${projectID} never contained ${marker}`)
}

async function waitForProjectAppOnPort(request: APIRequestContext, id: string, port: number) {
  for (let attempt = 0; attempt < 180; attempt++) {
    try {
      await execInProject(request, id, `curl -fsS http://127.0.0.1:${port}/ >/dev/null && curl -fsS http://127.0.0.1:4000/api/data >/dev/null`)
      return
    } catch {
      if (attempt % 10 === 0) {
        try {
          const npmStatus = await execInProject(request, id, 'cat /tmp/pcoder-npm.status 2>/dev/null || echo pending')
          if (npmStatus.trim() === '1') throw new Error('npm install failed')
        } catch (error) {
          if (error instanceof Error && error.message.startsWith('npm install failed')) throw error
        }
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 1000))
  }
  throw new Error(`React app in project ${id} never became ready`)
}

// Navigate from home to terminal view and open preview via the Preview tab.
// Real user flow: home → terminal → Preview tab → port → start → Open (new tab).
// port defaults to 3000 (the historical fixture default).
export async function openPreviewFromTerminal(page: Page, projectId: string, port = 3000): Promise<Page> {
  const card = page.getByTestId(`project-card-${projectId}`)
  await card.getByRole('button', { name: 'Terminal' }).click()
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('tab-preview').click()
  const portBtn = page.getByTestId(`preview-port-${port}`)
  await expect(portBtn).toBeVisible({ timeout: 60_000 })
  await portBtn.click()
  const openBtn = page.getByTestId(`preview-open-${port}`)
  await expect(openBtn).toBeVisible({ timeout: 30_000 })
  const [previewPage] = await Promise.all([
    page.context().waitForEvent('page'),
    openBtn.click(),
  ])
  await waitForPreview(previewPage)
  // The fresh popup may open behind the terminal tab, and background tabs
  // get their timers throttled — foreground it like a real user click would
  // so the preview page's fit-to-window sync runs promptly.
  await previewPage.bringToFront()
  return previewPage
}

export async function waitForPreview(page: Page) {
  const frame = page.locator('iframe[title="Remote project preview"]')
  await expect(frame.contentFrame().locator('canvas').first()).toBeVisible({ timeout: 60_000 })
  const connectBtn = frame.contentFrame().locator('text=Connect')
  if (await connectBtn.isVisible({ timeout: 5_000 }).catch(() => false)) {
    await connectBtn.click()
  }
  await page.waitForTimeout(5000)
}

export function pngSize(png: Buffer): { width: number; height: number } {
  // IHDR chunk: width at bytes 16-19, height at 20-23 (big-endian).
  return { width: png.readUInt32BE(16), height: png.readUInt32BE(20) }
}

// Poll the sidecar screenshot until the Chromium window matches the noVNC
// iframe box (the preview page auto-fits on open/resize, debounced). Without
// this, surface screenshots race the fit and flake between fitted/cropped.
export async function waitForChromiumFit(page: Page, request: APIRequestContext, projectId: string, token?: string) {
  token ??= await statusToken(request, projectId)
  // Foreground the tab first: background tabs get their timers throttled,
  // which stalls the preview page's debounced fit sync nondeterministically.
  await page.bringToFront()
  await expect(async () => {
    expect(await page.evaluate(() => document.visibilityState)).toBe('visible')
  }).toPass({ timeout: 10_000 })
  const box = await page.locator('iframe[title="Remote project preview"]').boundingBox()
  const want = { width: Math.round(box?.width ?? 0), height: Math.round(box?.height ?? 0) }
  await expect(async () => {
    const res = await request.get(`/api/projects/${projectURL(projectId)}/preview/tools/screenshot`, tokenHeaders(token))
    expect(res.ok()).toBeTruthy()
    const { width, height } = pngSize(Buffer.from(await res.body()))
    expect(Math.abs(width - want.width) <= 4).toBe(true)
    expect(Math.abs(height - want.height) <= 4).toBe(true)
  }).toPass({ timeout: 60_000 })
  return want
}

// ── htmx fixture ──
// Minimal htmx + Vite + backend, proves HMR works for non-React stacks.
const htmxFiles: Record<string, string> = {
  'package.json': JSON.stringify({
    private: true,
    scripts: { dev: 'vite' },
    dependencies: { vite: '5.4.14' },
  }),
  'index.html': [
    '<!DOCTYPE html><html><head><meta charset="utf-8"><script src="https://unpkg.com/htmx.org@1.9.12"></script></head>',
    '<body><main style="font-family:sans-serif;padding:32px">',
    '  <h1>HTMX App</h1>',
    '  <p data-testid="hmr-marker">HTMX initial</p>',
    '  <p data-testid="backend-data">loading...</p>',
    '  <button hx-get="http://localhost:4000/api/data" hx-target="#out" data-testid="htmx-btn">Fetch</button>',
    '  <div id="out"></div>',
    '  <script type="module" src="/src/main.js"></script>',
    '</main></body></html>',
  ].join('\n'),
  'src/main.js': [
    "const el = document.querySelector('[data-testid=\"backend-data\"]')",
    "fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{ el.textContent = JSON.stringify(d) }).catch(()=>el.textContent='fetch failed')",
    "if (import.meta.hot) import.meta.hot.accept()",
  ].join('\n'),
  'server.js': reactFiles['server.js'],
}

export async function createHtmxProject(request: APIRequestContext, slot = 1): Promise<string> {
  return createRunningViteProject(request, htmxFiles, slot)
}

// ── vanilla HTML fixture ──
// No build step, no framework — just plain HTML + JS served by a tiny
// Node http server. Proves the preview works without npm/Node build tools.
const vanillaFiles: Record<string, string> = {
  'index.html': [
    '<!DOCTYPE html><html><head><meta charset="utf-8"></head>',
    '<body style="font-family:sans-serif;padding:32px">',
    '  <h1>Vanilla HTML App</h1>',
    '  <p data-testid="hmr-marker">Vanilla initial</p>',
    '  <p data-testid="backend-data">loading...</p>',
    '  <button id="fetch-btn" data-testid="vanilla-btn">Fetch</button>',
    '  <div id="out"></div>',
    '  <script>',
    "    const el = document.querySelector('[data-testid=\"backend-data\"]');",
    "    fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{el.textContent=JSON.stringify(d)}).catch(()=>el.textContent='fetch failed');",
    '  </script>',
    '</body></html>',
  ].join('\n'),
  'server.js': reactFiles['server.js'],
  'static-server.js': [
    'const http = require("http")',
    'const fs = require("fs")',
    'const path = require("path")',
    'const srv = http.createServer((req, res) => {',
    '  let filePath = path.join(__dirname, req.url === "/" ? "index.html" : req.url)',
    '  fs.readFile(filePath, (err, data) => {',
    '    if (err) { res.writeHead(404); res.end("not found"); return }',
    '    const ext = path.extname(filePath)',
    '    const ct = {".html":"text/html",".js":"application/javascript",".css":"text/css"}[ext] || "text/plain"',
    '    res.writeHead(200, {"Content-Type": ct})',
    '    res.end(data)',
    '  })',
    '})',
    'srv.listen(3000, () => console.log("static:3000"))',
  ].join('\n'),
}

// ── static-file fixtures (no npm/Vite) ────────────────────────────────
// Vanilla (static server :3000 + Node backend :4000) and responsive (static
// server only) share this: write files, launch, wait until the ports answer.
async function createStaticProject(
  request: APIRequestContext,
  label: string,
  fixtureFiles: Record<string, string>,
  launch: string,
  probes: string[],
): Promise<string> {
  const id = await createProject(request)
  const command = `${writeFilesCmd(fixtureFiles)}; ${launch}`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  const check = probes.map((p) => `curl -fsS http://127.0.0.1:${p} >/dev/null`).join(' && ')
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      await execInProject(request, id, check)
      return id
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  }
  throw new Error(`${label} app in project ${id} never became ready`)
}

export async function createVanillaProject(request: APIRequestContext): Promise<string> {
  return createStaticProject(request, 'Vanilla', vanillaFiles, `nohup bash -lc '
    cd /workspace/app;
    node server.js >/tmp/pcoder-backend.log 2>&1 &
    exec node static-server.js >/tmp/pcoder-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`, ['3000/', '4000/api/data'])
}

// ── responsive fit fixture ──
// Deliberately loud media-query styles (blue DESKTOP row vs red PHONE
// column) plus a matchMedia-driven data-mode marker, so screenshots AND
// inspect HTML prove which layout viewport Chromium is really using.
// No timestamps anywhere — pixel baselines stay stable run to run.
const responsiveFiles: Record<string, string> = {
  'index.html': [
    '<!DOCTYPE html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">',
    '<title>Fit check</title>',
    '<style>',
    '  body { margin: 0; font-family: sans-serif; background: #1d4ed8; color: #fff; }',
    '  .wrap { padding: 32px; }',
    '  .mode { font-size: 40px; font-weight: 800; }',
    '  .cards { display: flex; gap: 16px; margin-top: 24px; }',
    '  .card { flex: 1; background: #fff; color: #111; border-radius: 12px; padding: 24px; font-size: 20px; }',
    '  @media (max-width: 600px) {',
    '    body { background: #b91c1c; }',
    '    .cards { flex-direction: column; }',
    '  }',
    '</style></head>',
    '<body data-mode="unknown">',
    '  <div class="wrap">',
    '    <div class="mode" data-testid="mode">...</div>',
    '    <div class="cards"><div class="card">Alpha</div><div class="card">Beta</div><div class="card">Gamma</div></div>',
    '  </div>',
    '  <script>',
    '    function sync() {',
    "      var phone = matchMedia('(max-width: 600px)').matches;",
    "      document.body.dataset.mode = phone ? 'phone' : 'desktop';",
    "      document.querySelector('[data-testid=\"mode\"]').textContent = phone ? 'PHONE' : 'DESKTOP';",
    '    }',
    "    matchMedia('(max-width: 600px)').addEventListener('change', sync);",
    '    sync();',
    '  </script>',
    '</body></html>',
  ].join('\n'),
  'static-server.js': vanillaFiles['static-server.js'],
}

export async function createResponsiveProject(request: APIRequestContext): Promise<string> {
  return createStaticProject(request, 'Responsive', responsiveFiles, `nohup bash -lc '
    cd /workspace/app;
    exec node static-server.js >/tmp/pcoder-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`, ['3000/'])
}

// ── Vue.js fixture ──
// Minimal Vue 3 SPA with Vite, proves HMR for another popular SPA framework.
const vueFiles: Record<string, string> = {
  'package.json': JSON.stringify({
    private: true,
    scripts: { dev: 'vite' },
    dependencies: { vue: '3.4.38', vite: '5.4.14' },
    devDependencies: { '@vitejs/plugin-vue': '5.1.2' },
  }),
  'vite.config.js': [
    "import { defineConfig } from 'vite'",
    "import vue from '@vitejs/plugin-vue'",
    'export default defineConfig({ plugins: [vue()] })',
  ].join('\n'),
  'index.html': '<div id="app"></div><script type="module" src="/src/main.js"></script>',
  'src/main.js': [
    "import { createApp, ref, onMounted } from 'vue'",
    "import App from './App.vue'",
    "createApp(App).mount('#app')",
  ].join('\n'),
  'src/App.vue': [
    '<template>',
    '  <main style="font-family:sans-serif;padding:32px">',
    '    <h1>Vue App</h1>',
    '    <p data-testid="hmr-marker">Vue initial</p>',
    '    <p data-testid="backend-data">{{ backendData }}</p>',
    '    <button data-testid="vue-btn" @click="fetchData">Fetch</button>',
    '  </main>',
    '</template>',
    '',
    '<script setup>',
    "import { ref, onMounted } from 'vue'",
    '',
    'const backendData = ref("loading...")',
    '',
    'onMounted(() => {',
    "  fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{ backendData.value = JSON.stringify(d) }).catch(()=>backendData.value='fetch failed')",
    '})',
    '',
    'function fetchData() {',
    "  fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{ backendData.value = JSON.stringify(d) }).catch(()=>backendData.value='fetch failed')",
    '}',
    '</script>',
  ].join('\n'),
  'server.js': reactFiles['server.js'],
}

export async function createVueProject(request: APIRequestContext): Promise<string> {
  return createRunningViteProject(request, vueFiles)
}

// -- preview token helpers (preview-token-rfc section 6) --
// The surface page mints a per-sidecar token; every tools call carries it.
export async function statusToken(request: APIRequestContext, id: string): Promise<string> {
  const res = await request.get(`/api/projects/${projectURL(id)}/preview`)
  expect(res.ok()).toBeTruthy()
  const body = (await res.json()) as { token?: string }
  expect(body.token).toBeTruthy()
  return body.token as string
}

export function tokenHeaders(token: string): { headers: Record<string, string> } {
  return { headers: { 'X-Preview-Token': token } }
}

export async function toolGet(request: APIRequestContext, id: string, tool: string, token: string) {
  return request.get(`/api/projects/${projectURL(id)}/preview/tools/${tool}`, tokenHeaders(token))
}

export async function surfaceToken(page: Page): Promise<string> {
  const src = await page.locator('iframe[title="Remote project preview"]').getAttribute('src')
  expect(src).toContain('token=')
  const u = new URL(src as string, 'http://x')
  return u.searchParams.get('token') as string
}

export async function openSurfacePage(browser: import("@playwright/test").Browser, id: string) {
  const ctx = await browser.newContext()
  const tab = await ctx.newPage()
  await tab.goto(`/preview/${encodeURIComponent(id)}`)
  await expect(tab.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
  return tab
}
