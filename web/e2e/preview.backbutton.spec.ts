import { expect, test } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createVanillaProject, openSurfacePage, statusToken, tokenHeaders } from './preview.helpers'

// Screenshot test for the mobile back button on the preview surface.
// On touch devices, swipe-back gestures are captured by the VNC canvas
// (cursor moves inside the remote desktop), so a back button must appear.
// The desktop case is covered by unit tests (isTouchDevice returns false
// without pointer:coarse or maxTouchPoints).
test.describe('preview surface back button (mobile only)', () => {
  // hasTouch: true sets navigator.maxTouchPoints and enables touch events,
  // which make isTouchDevice() return true regardless of viewport size.
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('shows back button on a phone viewport', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')

      // Open preview through terminal → Preview tab → Open in new tab.
      const card = page.getByTestId(`project-card-${projectID}`)
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

      // Wait for the iframe + noVNC surface to load.
      await expect(previewPage.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
      await previewPage.waitForTimeout(5_000)

      // On touch devices, the back button should be visible.
      const backBtn = previewPage.locator('button[aria-label="Back to terminal"]')
      await expect(backBtn).toBeVisible({ timeout: 10_000 })
      await expect(previewPage).toHaveScreenshot('preview-back-button-phone.png', { fullPage: true })

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('back button navigates back or to /app', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')

      const card = page.getByTestId(`project-card-${projectID}`)
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

      await expect(previewPage.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
      await previewPage.waitForTimeout(5_000)

      const backBtn = previewPage.locator('button[aria-label="Back to terminal"]')
      await expect(backBtn).toBeVisible({ timeout: 10_000 })

      // Capture the page's current URL before clicking back.
      const beforeURL = previewPage.url()

      await backBtn.click()

      // The back button either calls history.back() (navigates away from
      // /preview/<id>) or sets location.href to /app (same path change).
      // Either way, the URL must change within a few seconds.
      await expect(async () => {
        const afterURL = previewPage.url()
        expect(afterURL).not.toBe(beforeURL)
      }).toPass({ timeout: 10_000 })

      // The preview page should no longer show the iframe.
      await expect(previewPage.locator('iframe[title="Remote project preview"]')).toHaveCount(0, { timeout: 5_000 })
    } finally {
      await deleteAllProjects(request)
    }
  })
})
