import { expect, test } from './test'
import { mockConfigured } from './mocks'

// Pull-to-refresh on mobile PWA. The native iOS gesture is blocked by the
// sticky header, so usePullToRefresh() installs a JS handler that reloads the
// page when the user drags down at the top of the scroll. This test drives
// that handler with synthetic TouchEvents and asserts the reload fires (and
// does NOT fire when scrolled).
test.describe('pull-to-refresh (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  async function dispatchPull(page: import('@playwright/test').Page, startY: number, deltaY: number) {
    await page.evaluate(([sy, dy]) => {
      const startX = window.innerWidth / 2
      const target = document.body

      const t = (y: number): Touch => {
        if (typeof Touch !== 'undefined') {
          return new Touch({ identifier: 1, target, clientX: startX, clientY: y, screenX: startX, screenY: y })
        }
        return { identifier: 1, target, clientX: startX, clientY: y, screenX: startX, screenY: y } as unknown as Touch
      }

      const start = t(sy)
      target.dispatchEvent(new TouchEvent('touchstart', {
        touches: [start], targetTouches: [start], changedTouches: [start], bubbles: true, cancelable: true,
      }))

      for (let y = sy + 20; y <= sy + dy; y += 20) {
        const touch = t(y)
        target.dispatchEvent(new TouchEvent('touchmove', {
          touches: [touch], targetTouches: [touch], changedTouches: [touch], bubbles: true, cancelable: true,
        }))
      }

      target.dispatchEvent(new TouchEvent('touchend', {
        touches: [], targetTouches: [], changedTouches: [start], bubbles: true, cancelable: true,
      }))
    }, [startY, deltaY])
  }

  test('drag-down at top of scroll triggers page reload', async ({ page }) => {
    await mockConfigured(page)
      await page.goto('/app')
    await page.evaluate(() => window.scrollTo(0, 0))
    expect(await page.evaluate(() => window.scrollY)).toBe(0)
    await expect(page.getByTestId('butler-fab')).toBeVisible()

    // The reload replaces the document — detect via navigation event.
    const reloadPromise = page.waitForLoadState('domcontentloaded')
    // Threshold is 1/3 viewport height (≈281px at 844px) — a short 120px
    // flick must NOT reload, a deep 400px drag must.
    await dispatchPull(page, 80, 120) // short flick: no reload
    await page.waitForTimeout(500)
    await dispatchPull(page, 80, 400) // deep drag: reload
    await reloadPromise

    // After reload, the page should still render the home screen.
    await expect(page.getByTestId('butler-fab')).toBeVisible()
  })

  test('drag-down when scrolled does not reload', async ({ page }) => {
    await mockConfigured(page)
      await page.goto('/app')

    // Make the page scrollable so scrollY can be non-zero.
    await page.evaluate(() => {
      document.body.style.paddingBottom = '300vh'
      window.scrollTo(0, 200)
    })
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0)
    await expect(page.getByTestId('butler-fab')).toBeVisible()

    // Stamp a marker on window; reload clears it.
    const stamp = Date.now()
    await page.evaluate((s) => { (window as unknown as { __pullTestMarker: number }).__pullTestMarker = s }, stamp)

    // Trigger the pull — hook should refuse because scrollY !== 0.
    await dispatchPull(page, 280, 120)
    await page.waitForTimeout(800)

    // Marker persists → no reload happened.
    const marker = await page.evaluate(() => (window as unknown as { __pullTestMarker: number }).__pullTestMarker)
    expect(marker).toBe(stamp)
    expect(await page.evaluate(() => window.scrollY)).toBeGreaterThan(0)
  })

  test('drag-down starting on the FAB never reloads (FAB at top)', async ({ page }) => {
    await mockConfigured(page)
      await page.goto('/app')
    await page.evaluate(() => window.scrollTo(0, 0))
    expect(await page.evaluate(() => window.scrollY)).toBe(0)

    // Park the FAB at the top-right so the drag starts at the top edge —
    // the exact reported repro: FAB on top, drag it down, page refreshed.
    const fab = page.getByTestId('butler-fab')
    await expect(fab).toBeVisible()
    const box = await fab.boundingBox()
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
    await page.mouse.down()
    await page.mouse.move(box!.x + box!.width / 2, 40, { steps: 10 })
    await page.mouse.up()
    const topBox = await fab.boundingBox()
    expect(topBox!.y).toBeLessThan(50)

    // Stamp a marker on window; reload clears it.
    const stamp = Date.now()
    await page.evaluate((s) => { (window as unknown as { __pullTestMarker: number }).__pullTestMarker = s }, stamp)

    // Touch-drag straight down from the FAB, past 1/3 viewport.
    await page.evaluate(() => {
      const el = document.querySelector('[data-testid="butler-fab"]')!
      const r = el.getBoundingClientRect()
      const x = r.x + r.width / 2
      const t = (y: number): Touch =>
        new Touch({ identifier: 7, target: el, clientX: x, clientY: y, screenX: x, screenY: y })
      const fire = (type: string, y: number, touches: Touch[]) => {
        const start = t(y)
        el.dispatchEvent(new TouchEvent(type, {
          touches, targetTouches: touches, changedTouches: [start], bubbles: true, cancelable: true,
        }))
      }
      const sy = r.y + r.height / 2
      fire('touchstart', sy, [t(sy)])
      for (let y = sy + 20; y <= sy + 400; y += 20) fire('touchmove', y, [t(y)])
      fire('touchend', sy + 400, [])
    })
    await page.waitForTimeout(800)

    // Marker persists → no reload happened, and the FAB is still there.
    const marker = await page.evaluate(() => (window as unknown as { __pullTestMarker: number }).__pullTestMarker)
    expect(marker).toBe(stamp)
    await expect(page.getByTestId('butler-fab')).toBeVisible()
  })
})
