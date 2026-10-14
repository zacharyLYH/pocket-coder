import { ciTimeout, expect, test } from './test'

import { mockSessions } from './mocks'
import { terminalUrl } from './helpers'

// Scroll pin: a long terminal must scroll with the wheel — up shows earlier
// output, down returns to the latest — and scrolling must never synthesize
// input (the old bug: wheel leaked through as Up-arrow, recalling shell
// history instead of scrolling).
//
// Fully mocked at the browser edge (no Docker): the session ensure succeeds,
// the WS bridge is stubbed to emit 200 lines, and client→server frames are
// captured to prove no arrow-key input escapes during scroll.
const FAKE_ID = 'e2e/scroll-fake'

function lines(start: number, end: number): string {
  let out = ''
  for (let i = start; i <= end; i++) {
    out += `SCROLL-LINE-${String(i).padStart(3, '0')}\r\n`
  }
  return out
}

test.describe('terminal scroll', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('wheel over long output scrolls up and back down without sending input', async ({ page }) => {
    await mockSessions(page)

    const clientFrames: string[] = []
    await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
      let serverSent = false
      ws.onMessage((msg) => {
        clientFrames.push(msg.toString())
        // The client's first frame is the resize sent right after xterm
        // opens, so answering it means the terminal is ready — no sleep.
        if (!serverSent) {
          serverSent = true
          ws.send(JSON.stringify({ type: 'output', data: lines(1, 200) }))
        }
      })
    })

    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(15_000) })

    const rows = page.locator('.xterm-rows')
    await expect.poll(() => rows.innerText(), { timeout: ciTimeout(15_000) }).toContain('SCROLL-LINE-200')

    const bottomText = await rows.innerText()
    expect(bottomText).toContain('SCROLL-LINE-200')

    // Scroll up: the viewport must move to earlier output.
    const box = await page.locator('.xterm-screen').boundingBox()
    expect(box).toBeTruthy()
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
    const framesBefore = clientFrames.length
    await page.mouse.wheel(0, -1200)
    await expect
      .poll(async () => rows.innerText(), { timeout: ciTimeout(10_000) })
      .not.toEqual(bottomText)
    const topText = await rows.innerText()
    expect(topText).not.toContain('SCROLL-LINE-200')
    expect(topText).toContain('SCROLL-LINE-')

    // Scroll back down: the latest output returns.
    await page.mouse.wheel(0, 1200)
    await expect.poll(() => rows.innerText(), { timeout: ciTimeout(10_000) }).toContain('SCROLL-LINE-200')

    // The bug was wheel → Up/Down arrows (shell history recall). No input
    // frames carrying arrow escapes may escape during the scroll gestures.
    const scrollInputs = clientFrames.slice(framesBefore).filter((f) => f.includes('"input"'))
    expect(scrollInputs.filter((f) => f.includes('\\u001b[A') || f.includes('\\u001b[B'))).toEqual([])
    await expect(page).toHaveScreenshot('terminal-scroll.png')
  })

})

// Regression pin for the mobile bug: xterm has no touch scrolling of its
// own. Prod is `tmux attach` (alternate screen, no scrollback), so the pane
// bridges a one-finger drag into SGR mouse-wheel input (64 = wheel-up,
// 65 = wheel-down) — the same bytes a trusted desktop wheel produces — and
// tmux scrolls copy-mode with `mouse on`. The old scrollLines-only bridge
// passed a plain-output mock yet no-op'd on real phones, so this spec pins
// the wire bytes (not the viewport) and asserts scrollLines never runs on
// the alternate buffer. The terminal opts out of the document-level pull
// handler so the drag never reloads the page.
test.describe('terminal scroll (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  // Same helper shape as the desktop long-output spec — emits plain lines.
  // Prod wraps these in `tmux attach` (alternate screen); scrolling then
  // happens server-side in copy-mode, so the `.xterm-rows` viewport is NOT
  // expected to change here. What matters is the client→server SGR wheel
  // reports that drive that server-side scroll.
  async function touchDrag(page: import('@playwright/test').Page, deltaY: number) {
    await page.evaluate((dy) => {
      const screen = document.querySelector('.xterm-screen') as HTMLElement
      const r = screen.getBoundingClientRect()
      const x = r.x + r.width / 2
      const t = (y: number): Touch =>
        new Touch({ identifier: 5, target: screen, clientX: x, clientY: y, screenX: x, screenY: y })
      const fire = (type: string, y: number, touches: Touch[]) =>
        screen.dispatchEvent(new TouchEvent(type, { touches, targetTouches: touches, changedTouches: [t(y)], bubbles: true, cancelable: true }))
      const sy = r.y + r.height / 2
      fire('touchstart', sy, [t(sy)])
      const step = dy > 0 ? 20 : -20
      for (let y = sy + step; dy > 0 ? y <= sy + dy : y >= sy + dy; y += step) fire('touchmove', y, [t(y)])
      fire('touchend', sy + dy, [])
    }, deltaY)
  }

  test('one-finger drag sends SGR mouse-wheel input instead of refreshing', async ({ page }) => {
    await mockSessions(page)

    const clientFrames: string[] = []
    await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
      let serverSent = false
      ws.onMessage((msg) => {
        clientFrames.push(msg.toString())
        if (!serverSent) {
          serverSent = true
          // Enter alternate screen like `tmux attach` does, then emit output:
          // scrollLines is a noop here by design, scroll lives in tmux.
          ws.send(JSON.stringify({ type: 'output', data: '\x1b[?1049h' + lines(1, 200) }))
        }
      })
    })

    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(15_000) })
    const rows = page.locator('.xterm-rows')
    await expect.poll(() => rows.innerText(), { timeout: ciTimeout(15_000) }).toContain('SCROLL-LINE-200')

    // The pane opts out of the document-level pull handler.
    const optedOut = await page.evaluate(() => !!document.querySelector('.xterm-screen')?.closest('[data-no-pull-refresh]'))
    expect(optedOut).toBe(true)

    // Drag down → wheel-up reports (64) reveal earlier output in tmux…
    const framesBefore = clientFrames.length
    await touchDrag(page, 300)
    await expect.poll(() => clientFrames.filter((f) => f.includes('"input"') && f.includes('\\u001b[<64;')).length, { timeout: ciTimeout(10_000) }).toBeGreaterThan(0)
    // …never arrow-key leaks (the original desktop scroll bug)…
    const downInputs = clientFrames.slice(framesBefore).filter((f) => f.includes('"input"'))
    expect(downInputs.filter((f) => f.includes('\\u001b[A') || f.includes('\\u001b[B'))).toEqual([])
    // …and never scrollLines-shaped silence: every notch must emit input.
    expect(downInputs.length).toBeGreaterThan(0)
    await expect(page).toHaveScreenshot('terminal-scroll-mobile.png')

    // …and pushing back up sends wheel-down reports (65).
    const framesBeforeUp = clientFrames.length
    await touchDrag(page, -300)
    await expect.poll(() => clientFrames.slice(framesBeforeUp).filter((f) => f.includes('"input"') && f.includes('\\u001b[<65;')).length, { timeout: ciTimeout(10_000) }).toBeGreaterThan(0)
  })
})
