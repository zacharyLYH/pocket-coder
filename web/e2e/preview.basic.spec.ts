import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createReactProject, openPreviewFromTerminal, statusToken, tokenHeaders } from './preview.helpers'

test.describe('preview basic', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('full E2E: create project, terminal → Preview tab → Open, verify frontend+backend, interact, screenshot', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      // ── Step 1: Create a real full-stack project ──
      const projectID = await createReactProject(request)

      // ── Step 2: Verify project appears on home screen ──
      await page.goto('/')
      await expect(page.getByText(projectID)).toBeVisible({ timeout: 30_000 })

      // ── Step 3: Open preview via terminal → Preview tab → Open (new tab) ──
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const token = await statusToken(request, projectID)

      // ── Step 4: Verify frontend renders in noVNC ──
      const inspectRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('Full-Stack App')
      expect(html).toContain('name-input')

      // ── Step 5: Verify backend response (section 6.3 — direct fetch to localhost:4000) ──
      expect(html).toContain('backend-data')
      expect(html).toContain('source')

      // ── Step 6: Interact — type name and log in ──
      await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/type`, { ...tokenHeaders(token), 
        data: { selector: '[data-testid="name-input"]', text: 'E2E User' },
      })
      await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/click`, { ...tokenHeaders(token), 
        data: { selector: '[data-testid="login-btn"]' },
      })
      await page.waitForTimeout(2000)

      const afterLogin = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      const { html: loginHtml } = (await afterLogin.json()) as { html: string }
      expect(loginHtml).toContain('E2E User')
      expect(loginHtml).toContain('counter-value')

      // ── Step 7: Click increment ──
      await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/click`, { ...tokenHeaders(token), 
        data: { selector: '[data-testid="increment-btn"]' },
      })
      await page.waitForTimeout(1000)
      const afterIncr = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      const { html: incrHtml } = (await afterIncr.json()) as { html: string }
      expect(incrHtml).toContain('Count: 1')

      // ── Step 8: Capture AI screenshot ──
      const screenshotRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/screenshot`, tokenHeaders(token))
      expect(screenshotRes.ok()).toBeTruthy()
      expect(screenshotRes.headers()['content-type']).toContain('image/png')
      const pngBytes = Buffer.from(await screenshotRes.body())
      expect(pngBytes.length).toBeGreaterThan(1_000)
      expect(pngBytes[0]).toBe(0x89)

      // ── Step 9: Visual regression — desktop screenshot (preview tab) ──
      await expect(previewPage).toHaveScreenshot('preview-basic-desktop.png', { fullPage: true })
      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
