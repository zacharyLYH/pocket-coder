import { ciTimeout, expect, test, type APIRequestContext } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createVanillaProject, openPreviewFromTerminal, pngSize, statusToken, tokenHeaders } from './preview.helpers'

// The sidecar's X screen IS the VNC framebuffer: x11vnc encodes it and the
// noVNC client allocates it. These tests measure it on the client (the canvas
// backing store is sized to the framebuffer) and cross-check the server's own
// report, so the pixel budget is pinned against reality rather than against
// the sizing function's own arithmetic (that part is unit-tested).
//
// Per-frame pixel cost is the number that matters: every full frame — the
// initial paint, a reconnect, any full-screen damage — is framebuffer-sized.
const LEGACY_PIXELS = 1920 * 1080

type Measurement = {
  /** Framebuffer pixels the noVNC client is decoding (the X screen). */
  framebuffer: { width: number; height: number }
  /** CSS size the canvas is drawn at: the iframe box when scaling works. */
  canvas: { width: number; height: number }
  /** The iframe box the preview surface syncs Chromium to. */
  box: { width: number; height: number }
  dpr: number
}

// Scaling, not cropping: the canvas (framebuffer sized) must fit inside the
// iframe box, and fill it closely — a spike of letterbox means the framebuffer
// aspect no longer matches the client, and a canvas bigger than the box means
// the client is cropping pixels nobody can see.
function expectCanvasFitsBox(m: Measurement) {
  const fit = (got: number, box: number) => got / box
  for (const [axis, got, box] of [
    ['width', m.canvas.width, m.box.width],
    ['height', m.canvas.height, m.box.height],
  ] as const) {
    const ratio = fit(got, box)
    expect(ratio, `${axis}: canvas ${got} vs box ${box} (framebuffer ${m.framebuffer.width}x${m.framebuffer.height} @${m.dpr}x)`)
      .toBeLessThanOrEqual(1.02)
    expect(ratio, `${axis}: canvas ${got} vs box ${box}`).toBeGreaterThan(0.9)
  }
}

async function measure(previewPage: import('@playwright/test').Page): Promise<Measurement> {
  const frame = previewPage.locator('iframe[title="Remote project preview"]')
  const canvas = frame.contentFrame().locator('#screen canvas')
  await expect(canvas).toBeVisible({ timeout: ciTimeout(60_000) })
  const canvasSize = await canvas.evaluate((el: HTMLCanvasElement) => ({
    backing: { width: el.width, height: el.height },
    css: { width: el.clientWidth, height: el.clientHeight },
  }))
  const box = await frame.boundingBox()
  return {
    framebuffer: canvasSize.backing,
    canvas: canvasSize.css,
    box: { width: Math.round(box?.width ?? 0), height: Math.round(box?.height ?? 0) },
    dpr: await previewPage.evaluate(() => window.devicePixelRatio),
  }
}

// The server reports the screen it launched (and bounds the window by), so a
// mismatch against the client's measurement is a real bug, not a rounding.
// It re-applies the same viewport the surface asks for, so the cross-check
// leaves the window where the surface wanted it rather than clobbering the
// fit that the screenshot assertion below depends on.
async function reportedFramebuffer(
  request: APIRequestContext,
  projectID: string,
  token: string,
  m: Measurement,
) {
  const res = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, {
    ...tokenHeaders(token),
    data: { width: m.box.width, height: m.box.height, dpr: m.dpr },
  })
  expect(res.ok()).toBeTruthy()
  const body = (await res.json()) as {
    deviceScaleFactor: number
    framebuffer: { width: number; height: number }
  }
  expect(body.deviceScaleFactor).toBe(Math.min(m.dpr, 2))
  return body
}

// A DPR-2 phone used to be handed a 1920x1080 framebuffer — 6.3x the pixels
// its 390x844 viewport can show, of which it could display a 1:1 corner crop.
test.describe('preview framebuffer (phone)', () => {
  test.use({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, hasTouch: true })

  test('the framebuffer is the viewport at DPR, not a fixed 1920x1080', async ({ page, request }) => {
    test.setTimeout(ciTimeout(600_000))
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')
      // The start request is what creates the screen, so read the viewport
      // from the page that sends it.
      const clientView = await page.evaluate(() => ({
        width: window.innerWidth,
        height: window.innerHeight,
        dpr: window.devicePixelRatio,
      }))
      expect(clientView.dpr).toBe(2)

      const previewPage = await openPreviewFromTerminal(page, projectID)
      const token = await statusToken(request, projectID)
      const m = await measure(previewPage)
      const pixels = m.framebuffer.width * m.framebuffer.height

      // Sized from the client, not from a constant.
      expect(m.framebuffer).not.toEqual({ width: 1920, height: 1080 })
      expect(m.framebuffer.width).toBe(clientView.width * 2)
      expect(m.framebuffer.height).toBe(clientView.height * 2)

      // The performance claim: fewer pixels per frame than the fixed screen.
      expect(pixels).toBeLessThanOrEqual(LEGACY_PIXELS * 0.7)
      // ...and no waste above what this client actually displays.
      expect(pixels).toBeLessThanOrEqual(clientView.width * clientView.height * 4 * 1.1)

      // The client scales the framebuffer into its box (vnc_lite's `scale`
      // param): an unscaled canvas would be framebuffer-sized CSS and the
      // phone would see only the top-left corner of it.
      expectCanvasFitsBox(m)

      // Two independent sources agree on the screen size.
      const reported = await reportedFramebuffer(request, projectID, token, m)
      expect(reported.framebuffer).toEqual(m.framebuffer)

      // DPR reached the renderer: Chromium renders the CSS viewport at 2x, so
      // the frame the phone scales down is a retina one.
      await expect(async () => {
        const res = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/screenshot`, tokenHeaders(token))
        expect(res.ok()).toBeTruthy()
        const size = pngSize(Buffer.from(await res.body()))
        const want = { width: m.box.width * 2, height: m.box.height * 2 }
        const seen = `screenshot ${size.width}x${size.height}, want ${want.width}x${want.height} (box ${m.box.width}x${m.box.height} @2x)`
        expect(Math.abs(size.width - want.width), seen).toBeLessThanOrEqual(4)
        expect(Math.abs(size.height - want.height), seen).toBeLessThanOrEqual(4)
      }).toPass({ timeout: ciTimeout(60_000) })

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})

test.describe('preview framebuffer (desktop)', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('the framebuffer tracks a desktop window at DPR 1', async ({ page, request }) => {
    test.setTimeout(ciTimeout(600_000))
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVanillaProject(request)
      await page.goto('/app')
      const clientView = await page.evaluate(() => ({
        width: window.innerWidth,
        height: window.innerHeight,
        dpr: window.devicePixelRatio,
      }))
      expect(clientView.dpr).toBe(1)

      const previewPage = await openPreviewFromTerminal(page, projectID)
      const m = await measure(previewPage)

      expect(m.framebuffer).toEqual({ width: clientView.width, height: clientView.height })
      // 1280x720 is 44% of the old fixed screen.
      expect(m.framebuffer.width * m.framebuffer.height).toBeLessThanOrEqual(LEGACY_PIXELS * 0.5)
      expectCanvasFitsBox(m)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
