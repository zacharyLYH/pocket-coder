import { expect, test, type APIRequestContext } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createReactProject, openPreviewFromTerminal } from './preview.helpers'

function pngSize(png: Buffer): { width: number; height: number } {
  // IHDR chunk: width at bytes 16-19, height at 20-23 (big-endian).
  return { width: png.readUInt32BE(16), height: png.readUInt32BE(20) }
}

async function cdpScreenshot(request: APIRequestContext, projectID: string): Promise<Buffer> {
  const res = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
  expect(res.ok()).toBeTruthy()
  const body = Buffer.from(await res.body())
  expect(body.length).toBeGreaterThan(1_000)
  return body
}

test.describe('preview chromium viewports', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('chromium desktop + phone viewports via CDP', async ({ page, request }, testInfo) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // Desktop: host viewport stays 1280x720 — only the CDP viewport changes Chromium.
      // (No pixel snapshot: the fixture renders Date.now(), so bytes differ every run.)
      const desktopRes = await request.post(`/api/projects/${projectID}/preview/tools/viewport`, {
        data: { width: 1280, height: 800, mobile: false },
      })
      expect(desktopRes.ok()).toBeTruthy()
      await page.waitForTimeout(1000)
      const desktopPNG = await cdpScreenshot(request, projectID)
      expect(pngSize(desktopPNG)).toEqual({ width: 1280, height: 800 })
      await testInfo.attach('chromium-desktop', { body: desktopPNG, contentType: 'image/png' })

      // Phone: same host window, Chromium emulates a phone viewport.
      const phoneRes = await request.post(`/api/projects/${projectID}/preview/tools/viewport`, {
        data: { width: 390, height: 844, mobile: true },
      })
      expect(phoneRes.ok()).toBeTruthy()
      await page.waitForTimeout(1000)
      const phonePNG = await cdpScreenshot(request, projectID)
      expect(pngSize(phonePNG)).toEqual({ width: 390, height: 844 })
      await testInfo.attach('chromium-phone', { body: phonePNG, contentType: 'image/png' })

      // The two renders must actually differ (narrow vs wide layout).
      expect(phonePNG.equals(desktopPNG)).toBe(false)
      expect(phonePNG.length).toBeLessThan(desktopPNG.length)

      // Invalid sizes are rejected.
      const badRes = await request.post(`/api/projects/${projectID}/preview/tools/viewport`, {
        data: { width: 10, height: 10 },
      })
      expect(badRes.status()).toBe(400)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
