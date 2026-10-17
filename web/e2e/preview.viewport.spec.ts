import { ciTimeout, expect, test, type APIRequestContext } from './test'

import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createReactProject, openPreviewFromTerminal, waitForChromiumFit, pngSize, statusToken, tokenHeaders } from './preview.helpers'

async function cdpScreenshot(request: APIRequestContext, projectID: string, token: string): Promise<Buffer> {
  const res = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/screenshot`, tokenHeaders(token))
  expect(res.ok()).toBeTruthy()
  const body = Buffer.from(await res.body())
  expect(body.length).toBeGreaterThan(1_000)
  return body
}

// The sidecar's X screen is the client's viewport x DPR (see
// preview.framebuffer.spec.ts), so a viewport request larger than the screen
// that asked for the preview is clamped, and 1920x1080 is no longer a
// constant anywhere. This host viewport is chosen to leave room for both a
// desktop and a phone viewport request inside the screen it produces.
test.describe('preview chromium viewports', () => {
  test.use({ viewport: { width: 1280, height: 900 } })

  test('chromium desktop + phone viewports via CDP', async ({ page, request }, testInfo) => {
    test.setTimeout(ciTimeout(600_000))
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

      // Desktop: host viewport stays 1280x720 — only the CDP viewport changes Chromium.
      // (No pixel snapshot: the fixture renders Date.now(), so bytes differ every run.)
      const desktopRes = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, { ...tokenHeaders(token), 
        data: { width: 1280, height: 800, mobile: false },
      })
      expect(desktopRes.ok()).toBeTruthy()
      await page.waitForTimeout(1000)
      const desktopPNG = await cdpScreenshot(request, projectID, token)
      expect(pngSize(desktopPNG)).toEqual({ width: 1280, height: 800 })
      await testInfo.attach('chromium-desktop', { body: desktopPNG, contentType: 'image/png' })

      // Phone: same host window, Chromium emulates a phone viewport.
      const phoneRes = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, { ...tokenHeaders(token), 
        data: { width: 390, height: 844, mobile: true },
      })
      expect(phoneRes.ok()).toBeTruthy()
      await page.waitForTimeout(1000)
      const phonePNG = await cdpScreenshot(request, projectID, token)
      expect(pngSize(phonePNG)).toEqual({ width: 390, height: 844 })
      await testInfo.attach('chromium-phone', { body: phonePNG, contentType: 'image/png' })

      // The two renders must actually differ (narrow vs wide layout).
      expect(phonePNG.equals(desktopPNG)).toBe(false)
      expect(phonePNG.length).toBeLessThan(desktopPNG.length)

      // Oversize requests clamp to the sidecar screen instead of failing.
      const bigRes = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, { ...tokenHeaders(token), 
        data: { width: 3000, height: 2000 },
      })
      expect(bigRes.ok()).toBeTruthy()
      const bigBody = (await bigRes.json()) as {
        width: number; height: number; framebuffer: { width: number; height: number }
      }
      // The clamp target is the screen this preview actually got — the
      // viewport that started it, not a fixed 1920x1080.
      expect(bigBody.framebuffer.width).toBe(1280)
      expect(bigBody.framebuffer.height).toBe(900)
      expect([bigBody.width, bigBody.height]).toEqual([bigBody.framebuffer.width, bigBody.framebuffer.height])
      const bigPNG = await cdpScreenshot(request, projectID, token)
      expect(pngSize(bigPNG)).toEqual(bigBody.framebuffer)

      // Invalid sizes are rejected.
      const badRes = await request.post(`/api/projects/${projectURL(projectID)}/preview/tools/viewport`, { ...tokenHeaders(token), 
        data: { width: 10, height: 10 },
      })
      expect(badRes.status()).toBe(400)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('chromium follows the preview window size', async ({ page, request }, testInfo) => {
    test.setTimeout(ciTimeout(600_000))
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

      // Auto-fit on open: the Chromium window settles to the iframe box.
      const openWant = await waitForChromiumFit(previewPage, request, projectID, token)
      const widePNG = await cdpScreenshot(request, projectID, token)
      await testInfo.attach('chromium-follows-wide', { body: widePNG, contentType: 'image/png' })

      // Shrink the host window: the iframe shrinks and Chromium must follow,
      // so the app reflows instead of cropping a fixed-size desktop.
      await previewPage.setViewportSize({ width: 800, height: 600 })
      const narrowWant = await waitForChromiumFit(previewPage, request, projectID, token)
      expect(narrowWant.width).toBeLessThan(openWant.width - 100)
      const narrowPNG = await cdpScreenshot(request, projectID, token)
      await testInfo.attach('chromium-follows-narrow', { body: narrowPNG, contentType: 'image/png' })
      expect(pngSize(narrowPNG).width).toBeLessThan(pngSize(widePNG).width - 100)

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
