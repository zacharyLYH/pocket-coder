import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'
import { createVanillaProject, openPreviewFromTerminal } from './preview.helpers'

test.describe('preview vanilla HTML', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('vanilla HTML with Python http.server and backend', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // Verify vanilla HTML renders
      await expect(previewPage).toHaveScreenshot('preview-vanilla-initial.png', { fullPage: true })

      // Verify backend fetch worked via CDP
      const inspectRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('Vanilla HTML App')
      expect(html).toContain('Vanilla initial')
      expect(html).toContain('backend-data')
      // Backend data should be loaded (not just "loading...")
      expect(html).not.toMatch(/data-testid="backend-data">loading\.\.\./)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
