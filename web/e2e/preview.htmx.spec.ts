import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createHtmxProject, openPreviewFromTerminal } from './preview.helpers'

test.describe('preview htmx', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  // One htmx pass is enough: both previous tests booted the same fixture
  // and flow ("precreated + inject" never actually used the precreated
  // path). This pins render + backend content + both screenshots.
  test('htmx full-stack renders with backend content', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createHtmxProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      await expect(previewPage).toHaveScreenshot('preview-htmx-initial.png', { fullPage: true })
      const inspect = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`)
      expect((await inspect.json()).html).toContain('HTMX')
      await expect(previewPage).toHaveScreenshot('preview-htmx-hmr.png', { fullPage: true })
      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
