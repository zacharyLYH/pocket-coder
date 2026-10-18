import { ciTimeout, expect, test, type Page } from './test'

import { mockSessions } from './mocks'
import { terminalUrl } from './helpers'

// Liveness + spaciousness of the pane itself. Two things must hold on a
// desktop AND a phone viewport:
//
//   1. Lively: a saturated palette on deep navy, airy light strokes — not
//      grey-on-near-black.
//   2. Spacious: the scrollbar is 7px and the text claims every other pixel
//      of the pane, on first paint and after every resize.
//
// Fully mocked at the browser edge (no Docker): the session ensure succeeds
// and the WS bridge replays scripted frames on the client's first frame (the
// resize sent right after xterm opens), so nothing here is timing-dependent.
const FAKE_ID = 'e2e/liveness-fake'

const DOTS = 400

function binaryOutput(text: string): Buffer {
  return Buffer.concat([Buffer.from([0]), Buffer.from(text, 'utf8')])
}

function colors(): string {
  let out = ''
  for (let i = 0; i < 200; i++) {
    out += `\x1b[32mPASS\x1b[0m \x1b[34msrc/lib/thing.ts\x1b[0m \x1b[31mFAIL\x1b[0m \x1b[1mbold\x1b[0m ${String(i).padStart(3, '0')}\r\n`
  }
  return out
}

// A line wider than any viewport: it wraps at the terminal's column count, so
// the rendered row length IS the column count (see dotsCols).
function dots(): string {
  return '.'.repeat(DOTS) + '\r\n'
}

type Metrics = {
  background: string
  passColor: string | null
  fontWeight: string
  scrollbarWidth: number
  screenWidth: number
  paneWidth: number
  overflowX: boolean
  dotCols: number
}

function metrics(page: Page): Promise<Metrics> {
  return page.evaluate(() => {
    const q = (s: string) => document.querySelector(s) as HTMLElement | null
    const rows = q('.xterm-rows')!
    const viewport = q('.xterm-viewport')!
    const screen = q('.xterm-screen')!
    const pass = [...rows.querySelectorAll('span')].find((s) => s.textContent === 'PASS')
    const dotRow = [...rows.children].find((r) => /^\.{20,}$/.test((r.textContent ?? '').trim()))
    return {
      // The theme paints the scrollable element; the viewport behind it keeps
      // xterm's own black rule.
      background: getComputedStyle(q('.xterm-scrollable-element')!).backgroundColor,
      passColor: pass ? getComputedStyle(pass).color : null,
      // xterm's generated sheet puts the configured weight on the row spans
      // (the row div itself stays 400).
      fontWeight: getComputedStyle(pass ?? (rows.children[0] as HTMLElement)).fontWeight,
      scrollbarWidth: q('.scrollbar.vertical')?.getBoundingClientRect().width ?? 0,
      screenWidth: Math.round(screen.getBoundingClientRect().width),
      paneWidth: viewport.clientWidth,
      overflowX: viewport.scrollWidth > viewport.clientWidth,
      dotCols: (dotRow?.textContent ?? '').replace(/\s+$/, '').length,
    }
  })
}

// gutter is what the pane withholds from the text: the scrollbar plus the
// partial last cell.
function gutter(m: Metrics): number {
  return m.paneWidth - m.screenWidth
}

// dial stubs the session endpoints + WS and hands back the client→server
// frames. Every frame in `frames` is sent in one task, which also exercises
// the pane's per-frame write coalescing.
async function dial(page: Page, frames: string[]): Promise<string[]> {
  await mockSessions(page)
  const clientFrames: string[] = []
  await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
    let sent = false
    ws.onMessage((msg) => {
      clientFrames.push(msg.toString())
      if (!sent) {
        sent = true
        for (const data of frames) ws.send(binaryOutput(data))
      }
    })
  })
  await page.goto(terminalUrl(FAKE_ID, 'main'))
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(15_000) })
  return clientFrames
}

test('binary frames preserve terminal text and cut ANSI-heavy wire bytes', async ({ page }) => {
  await mockSessions(page)
  const text = '\x1b[32mPASS ✓\x1b[0m\r\n中文 🙂\r\n' + '\x1b[34mANSI\x1b[0m '.repeat(100)
  const frame = binaryOutput(text)
  const legacyBytes = Buffer.byteLength(JSON.stringify({ type: 'output', data: text }))

  await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
    let sent = false
    ws.onMessage(() => {
      if (sent) return
      sent = true
      ws.send(frame)
    })
  })
  await page.goto(terminalUrl(FAKE_ID, 'main'))
  const rows = page.locator('.xterm-rows')
  await expect(rows).toContainText('PASS ✓', { timeout: ciTimeout(15_000) })
  await expect(rows).toContainText('中文 🙂', { timeout: ciTimeout(15_000) })
  expect(frame.byteLength).toBeLessThan(legacyBytes * 0.6)
})

for (const [name, viewport] of [
  ['desktop', { width: 1280, height: 720 }],
  ['mobile', { width: 390, height: 844 }],
] as const) {
  test.describe(`terminal liveness (${name})`, () => {
    test.use(name === 'mobile' ? { viewport, hasTouch: true, isMobile: true } : { viewport })

    test('lively palette, airy text, 7px scrollbar and no dead gutter', async ({ page }) => {
      await dial(page, [colors()])
      await expectColored(page)

      const m = await metrics(page)
      // Liveness: deep navy instead of near-black, saturated ANSI green
      // instead of the old muted #91b362 / grey #b3b1ad foreground.
      expect(m.background).toBe('rgb(26, 27, 38)')
      expect(m.passColor).toBe('rgb(158, 206, 106)')
      // Air: light strokes on a 1.3 line height.
      expect(m.fontWeight).toBe('300')
      // Spaciousness: the slim 7px scrollbar, and no horizontal overflow.
      // The exact column budget is pinned in the refit test below.
      expect(m.scrollbarWidth).toBe(7)
      expect(m.overflowX).toBe(false)

      await expect(page.getByTestId('terminal-pane-wrap')).toHaveScreenshot(`terminal-liveness-${name}.png`)
    })

    test('columns refit to the pane on every resize', async ({ page }) => {
      await dial(page, [dots()])
      await expect.poll(() => metrics(page).then((m) => m.dotCols), { timeout: ciTimeout(15_000) }).toBeGreaterThan(20)

      const wide = await metrics(page)
      const cellWidth = wide.screenWidth / wide.dotCols

      // Shrink the viewport: the terminal must re-fit to the new width, not
      // keep its old column count (which left a dead gutter and made long
      // lines overflow the pane).
      await page.setViewportSize({ width: Math.round(viewport.width * 0.6), height: viewport.height })
      await expect.poll(() => metrics(page).then((m) => m.dotCols), { timeout: ciTimeout(10_000) }).toBeLessThan(wide.dotCols)

      const narrow = await metrics(page)
      expect(narrow.paneWidth).toBeLessThan(wide.paneWidth)
      // cols === floor(available width / cell width), the 7px scrollbar being
      // the only thing taken off the top.
      expect(narrow.dotCols).toBe(Math.floor((narrow.paneWidth - 7) / cellWidth))
      // gutter = the scrollbar + the partial last cell, never a reserved
      // column budget (the 14px default left ~18px here).
      expect(gutter(narrow)).toBeGreaterThanOrEqual(7)
      expect(gutter(narrow)).toBeLessThan(8 + cellWidth)
      expect(narrow.overflowX).toBe(false)

      // …and back: the columns expand to fill again.
      await page.setViewportSize(viewport)
      await expect.poll(() => metrics(page).then((m) => m.dotCols), { timeout: ciTimeout(10_000) }).toBe(wide.dotCols)
      const back = await metrics(page)
      expect(back.dotCols).toBe(Math.floor((back.paneWidth - 7) / cellWidth))
      expect(gutter(back)).toBeLessThan(8 + cellWidth)
      expect(back.overflowX).toBe(false)
    })

    test('a burst of output renders in full, in order, and input still flows', async ({ page }) => {
      const frames = Array.from({ length: 30 }, (_, i) => `BURST-${String(i).padStart(2, '0')}\r\n`)
      const clientFrames = await dial(page, frames)
      const rows = page.locator('.xterm-rows')
      await expect.poll(() => rows.innerText(), { timeout: ciTimeout(15_000) }).toContain('BURST-29')

      // Nothing dropped, nothing reordered: coalescing is a transport change,
      // never a content change.
      const text = await rows.innerText()
      const seen = [...text.matchAll(/BURST-(\d\d)/g)].map((m) => Number(m[1]))
      expect(seen.length).toBeGreaterThanOrEqual(24) // a full viewport of burst lines
      expect(seen).toEqual([...seen].sort((a, b) => a - b))
      expect(seen.at(-1)).toBe(29)

      // The input path survives the flood.
      await page.keyboard.type('x')
      await expect.poll(() => clientFrames.filter((f) => f.includes('"input"') && f.includes('"x"')).length, { timeout: ciTimeout(5_000) }).toBeGreaterThan(0)
    })
  })
}

async function expectColored(page: Page) {
  await expect.poll(() => page.locator('.xterm-rows').innerText(), { timeout: ciTimeout(15_000) }).toContain('PASS')
}
