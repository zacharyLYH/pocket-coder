import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createResponsiveProject, openPreviewFromTerminal, waitForChromiumFit } from './preview.helpers'

// The responsive fixture renders TOTALLY different styles per layout
// viewport (blue DESKTOP row vs red PHONE column, plus a matchMedia-driven
// data-mode marker), so these shots prove the sidecar window really follows
// the preview window instead of cropping a fixed-size desktop.
test.describe('preview window fit', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('responsive page reflows: desktop vs phone styles', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createResponsiveProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // Wide window: desktop styles.
      await waitForChromiumFit(previewPage, request, projectID)
      const desktopRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(desktopRes.ok()).toBeTruthy()
      expect(((await desktopRes.json()) as { html: string }).html).toContain('data-mode="desktop"')
      await expect(previewPage).toHaveScreenshot('preview-fit-desktop.png', { fullPage: true })

      // Narrow window: the same Chromium must switch to phone styles.
      await previewPage.setViewportSize({ width: 390, height: 844 })
      await waitForChromiumFit(previewPage, request, projectID)
      const phoneRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(phoneRes.ok()).toBeTruthy()
      expect(((await phoneRes.json()) as { html: string }).html).toContain('data-mode="phone"')
      await expect(previewPage).toHaveScreenshot('preview-fit-phone.png', { fullPage: true })

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
