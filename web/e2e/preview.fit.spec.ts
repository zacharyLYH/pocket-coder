import { expect, test } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createResponsiveProject, openPreviewFromTerminal, waitForChromiumFit, statusToken, tokenHeaders } from './preview.helpers'

// The responsive fixture renders TOTALLY different styles per layout
// viewport (blue DESKTOP row vs red PHONE column, plus a matchMedia-driven
// data-mode marker), so these shots prove the sidecar window really follows
// the preview window instead of cropping a fixed-size desktop.
test.describe('preview window fit', () => {
  // hasTouch (without isMobile) emulates touch capability while keeping a
  // desktop pointer: the desktop shot stays back-button-free (wide viewport
  // fails the touch+small-screen gate) and the phone shot below exercises
  // the real touch path, back button included. isMobile must stay off: it
  // flips pointer to coarse, which would show the button on desktop too.
  test.use({ viewport: { width: 1280, height: 720 }, hasTouch: true })

  test('responsive page reflows: desktop vs phone styles', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createResponsiveProject(request)
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const token = await statusToken(request, projectID)

      // Wide window: desktop styles.
      await waitForChromiumFit(previewPage, request, projectID)
      const desktopRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      expect(desktopRes.ok()).toBeTruthy()
      expect(((await desktopRes.json()) as { html: string }).html).toContain('data-mode="desktop"')
      await expect(previewPage).toHaveScreenshot('preview-fit-desktop.png', { fullPage: true })

      // Narrow window: the same Chromium must switch to phone styles. The
      // surface re-evaluates its touch gate on resize, so the back button
      // appears here live — no reload, no dropped VNC session.
      await previewPage.setViewportSize({ width: 390, height: 844 })
      await waitForChromiumFit(previewPage, request, projectID)
      const phoneRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`, tokenHeaders(token))
      expect(phoneRes.ok()).toBeTruthy()
      expect(((await phoneRes.json()) as { html: string }).html).toContain('data-mode="phone"')
      await expect(previewPage.getByRole('button', { name: 'Back to terminal' })).toBeVisible({ timeout: 10_000 })
      await expect(previewPage).toHaveScreenshot('preview-fit-phone.png', { fullPage: true })

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
