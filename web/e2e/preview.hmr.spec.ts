import { mkdirSync, writeFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createReactProject, execInProject, openPreviewFromTerminal } from './preview.helpers'

test.describe('preview HMR', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('full HMR E2E: terminal → Preview tab → Open, before shot, edit file, after shot', async ({ page, request }, testInfo) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      // ── Step 1: Create a real full-stack project ──
      const projectID = await createReactProject(request)
      await page.goto('/')
      await expect(page.getByText('untitled')).toBeVisible({ timeout: 30_000 })

      // ── Step 2: Open preview via terminal → Preview tab → Open (new tab) ──
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // ── Step 3: Verify initial state via CDP ──
      const inspectRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('Full-Stack App')
      expect(html).toContain('source')
      expect(html).not.toContain('HMR is working')

      // ── Step 4: Capture BEFORE screenshot (CDP + display surface) ──
      const beforeCDP = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(beforeCDP.ok()).toBeTruthy()
      const beforeCDPBytes = Buffer.from(await beforeCDP.body())
      const beforeDisplay = await previewPage.screenshot({ fullPage: true })

      const outDir = testInfo.outputPath('')
      mkdirSync(outDir, { recursive: true })
      writeFileSync(`${outDir}/preview-hmr-before-cdp.png`, beforeCDPBytes)
      writeFileSync(`${outDir}/preview-hmr-before-display.png`, beforeDisplay)

      // ── Step 5: Track navigations — HMR must NOT reload ──
      let navigations = 0
      page.on('framenavigated', () => { navigations++ })

      // ── Step 6: Edit the source file inside the container ──
      const newApp = [
        "import React, { useState, useEffect } from 'react'",
        '',
        'export function App() {',
        '  const [user, setUser] = useState(null)',
        '  const [name, setName] = useState("")',
        '  const [count, setCount] = useState(0)',
        '  const [backendData, setBackendData] = useState(null)',
        '',
        '  useEffect(() => {',
        '    fetch("http://localhost:4000/api/data")',
        '      .then(r => r.json()).then(setBackendData)',
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
        '    <main data-testid="login-screen" style={{fontFamily:"sans-serif",padding:32,background:"#dbeafe"}}>',
        '      <h1>Full-Stack App</h1>',
        '      <p data-testid="hmr-marker">HMR is working</p>',
        '      <p data-testid="backend-data">{backendData ? JSON.stringify(backendData) : "loading..."}</p>',
        '      <form onSubmit={e => { e.preventDefault(); login() }}>',
        '        <input data-testid="name-input" value={name} onChange={e=>setName(e.target.value)} placeholder="Your name" />',
        '        <button data-testid="login-btn" type="submit">Log in</button>',
        '      </form>',
        '    </main>',
        '  )',
        '',
        '  return (',
        '    <main data-testid="app-screen" style={{fontFamily:"sans-serif",padding:32,background:"#dbeafe"}}>',
        '      <h1>Hello, <span data-testid="user-name">{user.name}</span>!</h1>',
        '      <p data-testid="hmr-marker">HMR is working</p>',
        '      <p data-testid="backend-data">{backendData ? JSON.stringify(backendData) : "loading..."}</p>',
        '      <p data-testid="counter-value">Count: {count}</p>',
        '      <button data-testid="increment-btn" onClick={increment}>+1</button>',
        '      <button data-testid="reset-btn" onClick={reset}>Reset</button>',
        '      <button data-testid="logout-btn" onClick={logout}>Log out</button>',
        '    </main>',
        '  )',
        '}',
      ].join('\n')
      const encoded = Buffer.from(newApp).toString('base64')
      await execInProject(
        request,
        projectID,
        `printf '%s' '${encoded}' | base64 -d > /workspace/app/src/App.jsx`,
      )

      // ── Step 7: Wait for the visible value to change (section 6.4) ──
      let found = false
      for (let i = 0; i < 60; i++) {
        await page.waitForTimeout(1000)
        const res = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
        if (res.ok()) {
          const { html: h } = (await res.json()) as { html: string }
          if (h.includes('HMR is working')) {
            found = true
            break
          }
        }
      }
      expect(found).toBeTruthy()

      // ── Step 8: Test must not ask SPS to reload (section 6.4) ──
      expect(navigations).toBe(0)

      // ── Step 9: Capture AFTER screenshot ──
      const afterCDP = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(afterCDP.ok()).toBeTruthy()
      const afterCDPBytes = Buffer.from(await afterCDP.body())
      const afterDisplay = await previewPage.screenshot({ fullPage: true })

      writeFileSync(`${outDir}/preview-hmr-after-cdp.png`, afterCDPBytes)
      writeFileSync(`${outDir}/preview-hmr-after-display.png`, afterDisplay)

      // ── Step 10: Compare the screenshots (section 6.4) ──
      expect(beforeCDPBytes.equals(afterCDPBytes)).toBeFalsy()
      expect(beforeDisplay.equals(afterDisplay)).toBeFalsy()
      await expect(previewPage).toHaveScreenshot('preview-hmr-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('section 6.5: user and AI share one browser', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage2 = await openPreviewFromTerminal(page, projectID)

      // ── CDP changes page → display surface shows it ──
      await request.post(`/api/projects/${projectID}/preview/tools/type`, {
        data: { selector: '[data-testid="name-input"]', text: 'AI User' },
      })
      await request.post(`/api/projects/${projectID}/preview/tools/click`, {
        data: { selector: '[data-testid="login-btn"]' },
      })
      await page.waitForTimeout(2000)

      const cdpInspect = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      const { html: cdpHtml } = (await cdpInspect.json()) as { html: string }
      expect(cdpHtml).toContain('AI User')

      const displayShot = await previewPage2.screenshot({ fullPage: true })
      expect(displayShot.length).toBeGreaterThan(1_000)

      const cdpShot = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(cdpShot.ok()).toBeTruthy()
      const cdpPng = Buffer.from(await cdpShot.body())
      expect(cdpPng.length).toBeGreaterThan(1_000)
      expect(cdpPng[0]).toBe(0x89)
      await expect(previewPage2).toHaveScreenshot('preview-hmr-shared.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })
})
