import { ciTimeout, expect, test } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createVanillaProject, openPreviewFromTerminal, statusToken, tokenHeaders } from './preview.helpers'

// The sidecar's desktop IS the VNC framebuffer: Xtigervnc encodes it and the
// noVNC client allocates it. The entry page (pcoder.html) sets resizeSession,
// so the desktop must equal the iframe's CSS box at 1x — not the box times
// DPR (the old render) and not a fixed screen (the old seed).
//
// These tests read the desktop off the client (the canvas backing store is the
// framebuffer) and cross-check it against the box, then drive real viewport
// changes and assert the desktop follows each one.

type Desktop = {
  /** Framebuffer pixels the noVNC client is decoding (the X desktop). */
  backing: { width: number; height: number }
  /** CSS size the canvas is drawn at: equal to backing when nothing is scaled. */
  css: { width: number; height: number }
}

async function measure(previewPage: import('@playwright/test').Page): Promise<Desktop> {
  const canvas = previewPage
    .locator('iframe[title="Remote project preview"]')
    .contentFrame()
    .locator('#screen canvas')
    .first()
  await expect(canvas).toBeVisible({ timeout: ciTimeout(60_000) })
  return canvas.evaluate((el: HTMLCanvasElement) => ({
    backing: { width: el.width, height: el.height },
    css: { width: el.clientWidth, height: el.clientHeight },
  }))
}

async function boxSize(previewPage: import('@playwright/test').Page) {
  const box = await previewPage.locator('iframe[title="Remote project preview"]').boundingBox()
  return { width: Math.round(box?.width ?? 0), height: Math.round(box?.height ?? 0) }
}

// The sync is debounced and travel over RFB, so poll until the desktop is the
// box; then assert 1:1 — a scaled canvas would mean the client is resampling
// instead of resizing the desktop.
async function expectDesktopIsBox(previewPage: import('@playwright/test').Page) {
  const box = await boxSize(previewPage)
  await expect(async () => {
    const d = await measure(previewPage)
    expect(d.backing.width, `desktop ${d.backing.width}x${d.backing.height} vs box ${box.width}x${box.height}`)
      .toBeGreaterThanOrEqual(box.width - 2)
    expect(d.backing.width).toBeLessThanOrEqual(box.width + 2)
    expect(d.backing.height).toBeGreaterThanOrEqual(box.height - 2)
    expect(d.backing.height).toBeLessThanOrEqual(box.height + 2)
  }).toPass({ timeout: ciTimeout(60_000) })
  const d = await measure(previewPage)
  expect(d.css).toEqual(d.backing)
  return d
}

test.describe('preview desktop (desktop client)', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('the desktop is the iframe box at 1x', async ({ page, request }) => {
    test.setTimeout(ciTimeout(600_000))
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const token = await statusToken(request, projectID)

      const desktop = await expectDesktopIsBox(previewPage)
      // Not a fixed legacy screen and not viewport x DPR.
      expect(desktop.backing).not.toEqual({ width: 1920, height: 1080 })

      // The server agrees: syncing the same box reports scale 1 and no
      // framebuffer ceiling (the desktop resizes, so there is nothing to
      // clamp against).
      const res = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, {
        ...tokenHeaders(token),
        data: { width: desktop.backing.width, height: desktop.backing.height },
      })
      expect(res.ok()).toBeTruthy()
      const body = (await res.json()) as { width: number; height: number; deviceScaleFactor: number; framebuffer?: unknown }
      expect(body.deviceScaleFactor).toBe(1)
      expect(body.framebuffer).toBeUndefined()
      expect({ width: body.width, height: body.height }).toEqual(desktop.backing)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('the desktop follows every window resize, both ways', async ({ page, request }) => {
    test.setTimeout(ciTimeout(600_000))
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      const wide = await expectDesktopIsBox(previewPage)

      // Narrow: the desktop must shrink, not stay stale (that was the whole
      // bug: a fixed screen plus a scaled canvas).
      await previewPage.setViewportSize({ width: 800, height: 600 })
      const narrow = await expectDesktopIsBox(previewPage)
      expect(narrow.backing.width).toBeLessThan(wide.backing.width - 100)

      // Back to wide: follows again, so nothing is stuck at the narrow size.
      await previewPage.setViewportSize({ width: 1440, height: 900 })
      const back = await expectDesktopIsBox(previewPage)
      expect(back.backing.width).toBeGreaterThan(narrow.backing.width + 100)
      expect(back.backing.width).toBeGreaterThanOrEqual(1438)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})

test.describe('preview desktop (retina phone client)', () => {
  test.use({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true })

  test('the desktop is the CSS box, not the box times DPR', async ({ page, request }) => {
    test.setTimeout(ciTimeout(600_000))
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // The client really is DPR 2 — and the desktop still matches the CSS box.
      expect(await previewPage.evaluate(() => window.devicePixelRatio)).toBe(2)
      const desktop = await expectDesktopIsBox(previewPage)
      const box = await boxSize(previewPage)
      expect(desktop.backing.width).toBeLessThan(box.width * 2 - 100)

      // Rotation: a new box drives a new desktop.
      await previewPage.setViewportSize({ width: 844, height: 390 })
      const rotated = await expectDesktopIsBox(previewPage)
      expect(rotated.backing.width).toBeGreaterThan(desktop.backing.width + 100)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
