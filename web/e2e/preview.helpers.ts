import { expect, type APIRequestContext, type Page } from '@playwright/test'

// Real full-stack fixture per devin-clone.md section 6.3:
//   frontend: localhost:3000, backend: localhost:4000
//   fetch("http://localhost:4000/api/data") — DIRECT, not through proxy.
// This proves Chromium shares the project's network namespace.`  
// Also includes login form, counter, and HMR (section 6.4).
const files: Record<string, string> = {
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

export async function createReactProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(files).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const command = `${writes}; nohup bash -lc '
    cd /workspace/app;
    npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/sps-npm.log 2>&1;
    echo $? >/tmp/sps-npm.status;
    node server.js >/tmp/sps-backend.log 2>&1 &
    exec npm run dev -- --host 0.0.0.0 --port 3000 >/tmp/sps-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  await waitForProjectApp(request, id)
  return id
}

// Precreated project with code but no running servers — quickCommands are set
// so the test can start them via inject (user-driven flow).
export async function createPrecreatedProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(files).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const install = `${writes}; cd /workspace/app && npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/sps-npm.log 2>&1; echo $? >/tmp/sps-npm.status`
  const res = await request.post('/api/projects/exec', { data: { projectIds: [id], command: install } })
  expect(res.ok()).toBeTruthy()
  for (let i = 0; i < 60; i++) {
    const s = await execInProject(request, id, 'cat /tmp/sps-npm.status 2>/dev/null || echo pending')
    if (s.trim() === '0') break
    if (s.trim() === '1') throw new Error('npm install failed')
    await new Promise((r) => setTimeout(r, 2000))
  }
  await request.patch(`/api/projects/${id}`, { data: { quickCommands: { dev: 'cd /workspace/app && npm run dev -- --host 0.0.0.0 --port 3000', backend: 'cd /workspace/app && nohup node server.js >/tmp/sps-backend.log 2>&1 &' } } })
  return id
}

export async function execInProject(request: APIRequestContext, id: string, command: string): Promise<string> {
  const response = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(response.ok()).toBeTruthy()
  const body = (await response.json()) as { results: { status: string; detail?: string }[] }
  expect(body.results[0]?.status).toBe('ok')
  return body.results[0]?.detail ?? ''
}

async function waitForProjectApp(request: APIRequestContext, id: string) {
  for (let attempt = 0; attempt < 180; attempt++) {
    try {
      await execInProject(request, id, 'curl -fsS http://127.0.0.1:3000/ >/dev/null && curl -fsS http://127.0.0.1:4000/api/data >/dev/null')
      return
    } catch {
      if (attempt % 10 === 0) {
        try {
          const npmStatus = await execInProject(request, id, 'cat /tmp/sps-npm.status 2>/dev/null || echo pending')
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
export async function openPreviewFromTerminal(page: Page, projectId: string): Promise<Page> {
  const card = page.getByTestId(`project-card-${projectId}`)
  await card.getByRole('button', { name: 'Terminal' }).click()
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
  await page.getByTestId('tab-preview').click()
  const portBtn = page.getByTestId('preview-port-3000')
  await expect(portBtn).toBeVisible({ timeout: 60_000 })
  await portBtn.click()
  const openBtn = page.getByTestId('preview-open-3000')
  await expect(openBtn).toBeVisible({ timeout: 30_000 })
  const [previewPage] = await Promise.all([
    page.context().waitForEvent('page'),
    openBtn.click(),
  ])
  await waitForPreview(previewPage)
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
  'server.js': files['server.js'],
}

export async function createHtmxProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(htmxFiles).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const command = `${writes}; nohup bash -lc '
    cd /workspace/app;
    npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/sps-npm.log 2>&1;
    echo $? >/tmp/sps-npm.status;
    node server.js >/tmp/sps-backend.log 2>&1 &
    exec npm run dev -- --host 0.0.0.0 --port 3000 >/tmp/sps-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  await waitForProjectApp(request, id)
  return id
}

export async function createHtmxPrecreatedProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(htmxFiles).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const install = `${writes}; cd /workspace/app && npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/sps-npm.log 2>&1; echo $? >/tmp/sps-npm.status`
  const res = await request.post('/api/projects/exec', { data: { projectIds: [id], command: install } })
  expect(res.ok()).toBeTruthy()
  for (let i = 0; i < 60; i++) {
    const s = await execInProject(request, id, 'cat /tmp/sps-npm.status 2>/dev/null || echo pending')
    if (s.trim() === '0') break
    if (s.trim() === '1') throw new Error('npm install failed')
    await new Promise((r) => setTimeout(r, 2000))
  }
  await request.patch(`/api/projects/${id}`, { data: { quickCommands: { dev: 'cd /workspace/app && npm run dev -- --host 0.0.0.0 --port 3000', backend: 'cd /workspace/app && nohup node server.js >/tmp/sps-backend.log 2>&1 &' } } })
  return id
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
  'server.js': files['server.js'],
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

export async function createVanillaProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(vanillaFiles).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const command = `${writes}; nohup bash -lc '
    cd /workspace/app;
    node server.js >/tmp/sps-backend.log 2>&1 &
    exec node static-server.js >/tmp/sps-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  await waitForVanillaApp(request, id)
  return id
}

async function waitForVanillaApp(request: APIRequestContext, id: string) {
  for (let attempt = 0; attempt < 60; attempt++) {
    try {
      await execInProject(request, id, 'curl -fsS http://127.0.0.1:3000/ >/dev/null && curl -fsS http://127.0.0.1:4000/api/data >/dev/null')
      return
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  }
  throw new Error(`Vanilla app in project ${id} never became ready`)
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
  'server.js': files['server.js'],
}

export async function createVueProject(request: APIRequestContext): Promise<string> {
  const created = await request.post('/api/projects', { data: {} })
  expect(created.status()).toBe(201)
  const { id } = (await created.json()) as { id: string }
  const writes = Object.entries(vueFiles).map(([name, content]) => {
    const encoded = Buffer.from(content).toString('base64')
    return `mkdir -p /workspace/app/$(dirname '${name}'); printf '%s' '${encoded}' | base64 -d > /workspace/app/'${name}'`
  }).join('; ')
  const command = `${writes}; nohup bash -lc '
    cd /workspace/app;
    npm install --no-audit --no-fund --fetch-retries=0 --fetch-timeout=10000 >/tmp/sps-npm.log 2>&1;
    echo $? >/tmp/sps-npm.status;
    node server.js >/tmp/sps-backend.log 2>&1 &
    exec npm run dev -- --host 0.0.0.0 --port 3000 >/tmp/sps-vite.log 2>&1
  ' >/dev/null 2>&1 </dev/null &`
  const setup = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(setup.ok()).toBeTruthy()
  await waitForProjectApp(request, id)
  return id
}
