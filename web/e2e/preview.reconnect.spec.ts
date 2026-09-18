import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createReactProject, execInProject, waitForInspectContaining, statusToken, tokenHeaders } from './preview.helpers'

test.describe('preview reconnect', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('project survives phone disconnect and reconnect preserves browser state', async ({ page, request }) => {
    test.setTimeout(300_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      const token = await statusToken(request, projectID)

      // ── First connection: open preview, type something ──
      await page.goto(`/preview/${encodeURIComponent(projectID)}`)
      const frame = page.locator('iframe[title="Remote project preview"]')
      await expect(frame.contentFrame().locator('canvas').first()).toBeVisible({ timeout: 60_000 })

      // Add an input to the fixture for state persistence testing
      const inputApp = [
        "import React, { useState } from 'react'",
        'export function App() {',
        '  const [v, setV] = useState("initial")',
        '  return <main style={{fontFamily:"sans-serif",padding:32}}>',
        '    <h1>Container React preview</h1>',
        '    <input data-testid="persist-input" value={v} onChange={e=>setV(e.target.value)} />',
        '    <p data-testid="persist-value">{v}</p>',
        '  </main>',
        '}',
      ].join('\n')
      const encoded = Buffer.from(inputApp).toString('base64')
      await execInProject(
        request,
        projectID,
        `printf '%s' '${encoded}' | base64 -d > /workspace/app/src/App.jsx`,
      )

      // Wait for HMR to apply
      await waitForInspectContaining(request, projectID, 'persist-input', 30)

      // Type something via CDP tools
      await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/type`, { ...tokenHeaders(token), 
        data: { selector: '[data-testid="persist-input"]', text: 'typed-state' },
      })
      await page.waitForTimeout(1000)

      // Verify it's there
      let inspectRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      let { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('typed-state')

      // ── Disconnect: close the page (simulates phone disconnect) ──
      const ctx = page.context()
      await page.close()

      // The project container and browser sidecar must stay alive
      await new Promise((r) => setTimeout(r, 3000))
      const statusRes = await request.get(`/api/projects/${projectURL(projectID)}/preview`)
      expect(statusRes.ok()).toBeTruthy()
      const status = await statusRes.json()
      expect(status.status).toBe('ready')

      // ── Reconnect: open a new page to the same preview ──
      const newPage = await ctx.newPage()
      await newPage.goto(`/preview/${encodeURIComponent(projectID)}`)
      const newFrame = newPage.locator('iframe[title="Remote project preview"]')
      await expect(newFrame.contentFrame().locator('canvas').first()).toBeVisible({ timeout: 60_000 })

      // The browser state (typed text) must persist through the disconnect
      inspectRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      expect(inspectRes.ok()).toBeTruthy()
      ;({ html } = (await inspectRes.json()) as { html: string })
      // The input value persists because it's in Chromium's DOM, not the phone's
      expect(html).toContain('typed-state')

      // Visual regression after reconnect
      await expect(newPage).toHaveScreenshot('preview-reconnect.png', { fullPage: true })

      await newPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
