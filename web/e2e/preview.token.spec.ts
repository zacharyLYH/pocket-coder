import { expect, test } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createReactProject, openPreviewFromTerminal, openSurfacePage, startPreview, statusToken, surfaceToken, toolGet } from './preview.helpers'

// The compose stack defaults to PCODER_PREVIEW_TOKEN_SILENCE=20s with a 1s
// sweep interval, so a closed surface's token dies within ~2s of these
// waits — the waits below only need to clear the silence threshold.
const TOKEN_SILENCE_MS = 25_000

// Golden e2e for preview-token-rfc §6: single-tab rotation, multi-tab
// persistence, fast tools gating (no waiting), and the expired-surface UI.
test.describe('preview token rotation', () => {
  test.use({ viewport: { width: 1280, height: 720 } })
  test.setTimeout(600_000)

  test('single-tab rotation: stale token dies, status mints a successor', async ({ browser, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      const page = await browser.newPage()
      // Same ssh-probe stub as e2e/test.ts (which only covers the page
      // fixture): without it the home SSH gate hides the project cards on
      // raw pages and the Terminal button below never appears.
      await page.route('**/api/ssh/test', (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, user: 'e2e' }) }),
      )
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const t1 = await surfaceToken(previewPage)
      expect(await statusToken(request, projectID)).toBe(t1)
      // Close only the surface page: it shares page's context (opened via
      // window.open), so closing the context would kill page too, which the
      // steps below still need. Closing the page stops its beats.
      await previewPage.close()
      // Silence past threshold + sweep margin rotates the token.
      await page.waitForTimeout(TOKEN_SILENCE_MS)
      const staleSurface = await request.get(
        `/api/projects/${projectURL(projectID)}/preview/vnc_lite.html?token=${t1}`,
      )
      expect(staleSurface.status()).toBe(404)
      const t2 = await statusToken(request, projectID)
      expect(t2).not.toBe(t1)
      // Reopen attaches with the successor.
      await page.goto(`/preview/${encodeURIComponent(projectID)}`)
      await expect(page.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
      expect(await surfaceToken(page)).toBe(t2)
      await page.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('multi-tab persistence: one open tab keeps the token alive', async ({ browser, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await startPreview(request, projectID)
      const tabA = await openSurfacePage(browser, projectID)
      const t1 = await surfaceToken(tabA)
      const tabB = await openSurfacePage(browser, projectID)
      expect(await surfaceToken(tabB)).toBe(t1)
      await tabA.close()
      await tabA.context().close().catch(() => {})
      // Past the silence threshold with B still beating: no rotation.
      await tabB.waitForTimeout(TOKEN_SILENCE_MS)
      expect(await statusToken(request, projectID)).toBe(t1)
      const beat = await request.post(
        `/api/projects/${projectURL(projectID)}/preview/heartbeat`,
        { headers: { 'X-Preview-Token': t1 } },
      )
      expect(beat.ok()).toBeTruthy()
      const surface = await request.get(
        `/api/projects/${projectURL(projectID)}/preview/vnc_lite.html?token=${t1}`,
      )
      expect(surface.ok()).toBeTruthy()
      await tabB.close()
      await new Promise((r) => setTimeout(r, TOKEN_SILENCE_MS))
      expect(await statusToken(request, projectID)).not.toBe(t1)
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('tools gating: valid token passes, wrong/missing 404s, ports stays open', async ({
    page,
    request,
  }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const token = await statusToken(request, projectID)
      expect((await toolGet(request, projectID, 'inspect', token)).ok()).toBeTruthy()
      const missing = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`)
      expect(missing.status()).toBe(404)
      expect(await missing.json()).toEqual({ error: 'not found' })
      const wrong = await toolGet(request, projectID, 'inspect', 'wrong')
      expect(wrong.status()).toBe(404)
      expect((await request.get(`/api/projects/${projectURL(projectID)}/preview/ports`)).ok()).toBeTruthy()
      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('expired surface shows the resume card (screenshot as proof)', async ({ browser, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await startPreview(request, projectID)
      const page = await browser.newPage()
      await page.goto(`/preview/${encodeURIComponent(projectID)}`)
      await expect(page.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
      // Laptop-sleep case: the page stays open but its beats stop reaching
      // the server, so the token rots past the silence window. (Blocking
      // rather than closing: a closed page can't show the expired card, and
      // live 5s beats would otherwise keep the token alive forever.)
      await page.route('**/preview/heartbeat', (route) => route.abort())
      await page.waitForTimeout(TOKEN_SILENCE_MS)
      await page.unroute('**/preview/heartbeat')
      await expect(
        page.getByText('Preview expired — the sidecar rotated its token.'),
      ).toBeVisible({ timeout: 60_000 })
      await expect(page).toHaveScreenshot('preview-expired.png', { fullPage: true })
      // Resume re-mints a token and reattaches to the same sidecar.
      await page.getByRole('button', { name: 'Resume' }).click()
      await expect(page.locator('iframe[title="Remote project preview"]')).toBeVisible({ timeout: 60_000 })
      await page.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})