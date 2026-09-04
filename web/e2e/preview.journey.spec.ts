import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'
import { createPrecreatedProject, execInProject } from './preview.helpers'

test.describe('preview user journey', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('full journey: precreated project → inject start → Preview tab → Open → HMR → backend check', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createPrecreatedProject(request)
      await page.goto('/')
      await expect(page.getByText('untitled')).toBeVisible({ timeout: 10_000 })
      await page.getByTestId(`project-card-${projectID}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      // inject start commands via quickCommands (backend + vite)
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-backend').click()
      await page.waitForTimeout(2000)
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-dev').click()
      await page.waitForTimeout(8000)
      // Wait for vite to actually be listening so the terminal screenshot
      // consistently shows the ready state instead of mid-startup output.
      await expect(async () => {
        const r = await request.get(`/api/projects/${projectID}/preview/ports`)
        expect(r.ok()).toBeTruthy()
        const { ports } = (await r.json()) as { ports: { port: number }[] }
        expect(ports.map((p) => p.port)).toContain(3000)
      }).toPass({ timeout: 60_000 })
      await page.waitForTimeout(1000)
      await expect(page).toHaveScreenshot('preview-journey-terminal-after-inject.png', { fullPage: true })
      // Preview tab
      await page.getByTestId('tab-preview').click()
      await page.getByTestId('preview-refresh').click()
      const portBtn = page.getByTestId('preview-port-3000')
      await expect(portBtn).toBeVisible({ timeout: 60_000 })
      await expect(page).toHaveScreenshot('preview-journey-ports.png', { fullPage: true })
      await portBtn.click()
      const openBtn = page.getByTestId('preview-open-3000')
      await expect(openBtn).toBeVisible({ timeout: 30_000 })
      const [previewPage] = await Promise.all([
        page.context().waitForEvent('page'),
        openBtn.click(),
      ])
      const frame = previewPage.locator('iframe[title="Remote project preview"]')
      await expect(frame.contentFrame().locator('canvas').first()).toBeVisible({ timeout: 60_000 })
      await previewPage.waitForTimeout(5000)
      await expect(previewPage).toHaveScreenshot('preview-journey-open.png', { fullPage: true })
      // verify backend state via slots and preview status
      const portsRes = await request.get(`/api/projects/${projectID}/preview/ports`)
      expect(portsRes.ok()).toBeTruthy()
      const { ports } = (await portsRes.json()) as { ports: { port: number }[] }
      expect(ports.map((p) => p.port)).toContain(3000)
      const statusRes = await request.get(`/api/projects/${projectID}/preview`)
      expect((await statusRes.json()).status).toBe('ready')
      // HMR: edit file
      const newApp = `import React from 'react'; export function App(){ return <main><h1>HMR Journey Works</h1><p data-testid="hmr-marker">HMR is working</p></main>}`
      const enc = Buffer.from(newApp).toString('base64')
      await execInProject(request, projectID, `printf '%s' '${enc}' | base64 -d > /workspace/app/src/App.jsx`)
      let found = false
      for (let i = 0; i < 60; i++) {
        await page.waitForTimeout(1000)
        const r = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
        if (r.ok() && ((await r.json()) as { html: string }).html.includes('HMR is working')) {
          found = true
          break
        }
      }
      expect(found).toBeTruthy()
      await expect(previewPage).toHaveScreenshot('preview-journey-hmr.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('multi preview: two ports → two previews', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createPrecreatedProject(request)
      await page.goto('/')
      await page.getByTestId(`project-card-${projectID}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-backend').click()
      await page.waitForTimeout(2000)
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-dev').click()
      await page.waitForTimeout(8000)
      await page.getByTestId('tab-preview').click()
      await page.getByTestId('preview-refresh').click()
      await expect(page.getByTestId('preview-port-3000')).toBeVisible({ timeout: 60_000 })
      await expect(page).toHaveScreenshot('preview-multi-ports.png', { fullPage: true })
      // start preview
      await page.getByTestId('preview-port-3000').click()
      await expect(page.getByTestId('preview-open-3000')).toBeVisible({ timeout: 30_000 })
      const [previewPage2] = await Promise.all([
        page.context().waitForEvent('page'),
        page.getByTestId('preview-open-3000').click(),
      ])
      await expect(previewPage2.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 30_000 })
      await expect(previewPage2).toHaveScreenshot('preview-multi-first.png', { fullPage: true })
      await expect(page).toHaveScreenshot('preview-multi-second-ready.png', { fullPage: true })
      const portsRes = await request.get(`/api/projects/${projectID}/preview/ports`)
      expect((await portsRes.json()).ports.length).toBeGreaterThanOrEqual(1)
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('preview close: terminate and backend cleared', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createPrecreatedProject(request)
      await page.goto('/')
      await page.getByTestId(`project-card-${projectID}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-backend').click()
      await page.waitForTimeout(2000)
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-run-dev').click()
      await page.waitForTimeout(8000)
      await page.getByTestId('tab-preview').click()
      await page.getByTestId('preview-refresh').click()
      await expect(page.getByTestId('preview-port-3000')).toBeVisible({ timeout: 60_000 })
      await page.getByTestId('preview-port-3000').click()
      await expect(page.getByTestId('preview-open-3000')).toBeVisible({ timeout: 30_000 })
      const [previewPage3] = await Promise.all([
        page.context().waitForEvent('page'),
        page.getByTestId('preview-open-3000').click(),
      ])
      await expect(previewPage3.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 30_000 })
      await expect(previewPage3).toHaveScreenshot('preview-close-before.png', { fullPage: true })
      await page.getByTestId('preview-close-3000').click()
      await page.waitForTimeout(2000)
      const statusRes = await request.get(`/api/projects/${projectID}/preview`)
      expect((await statusRes.json()).status).toBe('stopped')
      await expect(page).toHaveScreenshot('preview-close-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })
})
