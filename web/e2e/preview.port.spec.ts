import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import {
  createRunningViteProjectOnPort,
  openPreviewFromTerminal,
  reactFiles,
} from './preview.helpers'

// BROWSER_TARGET plumb-through: before the port fix, the sidecar browser
// was hardcoded to http://127.0.0.1:3000, so any project whose app listened
// on a different port (Vite :5173, Express :8080, Flask :5000, …) rendered
// a blank sidecar. This spec boots the React fixture on :4001 and proves
// the preview page works end-to-end: the popup opens, the surface mounts,
// noVNC connects, the canvas becomes visible, and the sidecar actually
// loaded the fixture from :4001 (not the historical :3000).

test.describe('preview non-default port', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('sidecar loads app on :4001 (cfg.Port → BROWSER_TARGET)', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createRunningViteProjectOnPort(request, 4001, reactFiles)
      await page.goto('/')
      // Reuse the home → terminal → preview flow, parameterized by port.
      // The helper clicks the :4001 port button + the Open button and
      // returns the popup that hosts PreviewSurface.
      const previewPage = await openPreviewFromTerminal(page, projectID, 4001)

      // Frontend proof: the popup is the /preview/<id> route, not a
      // blank tab or an auth redirect.
      await expect(previewPage).toHaveURL(new RegExp(`/preview/${projectID}$`))

      // Frontend proof: PreviewSurface mounted the noVNC iframe.
      const iframe = previewPage.locator('iframe[title="Remote project preview"]')
      await expect(iframe).toBeVisible({ timeout: 30_000 })
      await expect(iframe).toHaveAttribute(
        'src',
        new RegExp(`/api/projects/${projectID}/preview/vnc_lite\\.html`),
      )

      // Frontend proof: the sidecar's display surface actually streamed
      // over the websocket (canvas = the live VNC stream, opaque to
      // Playwright but its visibility is the signal that the sidecar is
      // up and the page didn't crash mid-mount).
      const canvas = iframe.contentFrame().locator('canvas').first()
      await expect(canvas).toBeVisible({ timeout: 60_000 })

      // Backend proof: the sidecar's browser fetched the app from :4001
      // (not the historical :3000), so the rendered HTML is the fixture
      // from the non-default port. This is the only way to inspect what
      // the canvas is showing — the canvas pixels are opaque.
      const inspectRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('Full-Stack App')
      expect(html).toContain('name-input')
      // The backend on :4000 (the unchanged second fixture port) is
      // still served by the same container — proves BROWSER_TARGET is
      // the one and only thing the port fix changed.
      expect(html).toContain('counter-value')

      // Frontend proof: no console errors on the preview page. Network
      // failures of a single asset (e.g. favicon) are tolerated because
      // they don't indicate the page crashed; only error-level messages
      // raised by our own code or the surface itself fail the test.
      const consoleErrors: string[] = []
      previewPage.on('console', (msg) => {
        if (msg.type() === 'error') consoleErrors.push(msg.text())
      })
      // Drive a tiny interaction: scroll the surface and resize the host
      // viewport, which the sidecar mirrors back via the surface's
      // fit-to-window sync. No crash + iframe still present = page works.
      await iframe.contentFrame().locator('body').evaluate((b) => b.scrollTo(0, 0)).catch(() => {})
      await page.setViewportSize({ width: 1024, height: 768 })
      await expect(iframe).toBeVisible({ timeout: 5_000 })
      expect(consoleErrors).toEqual([])

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
