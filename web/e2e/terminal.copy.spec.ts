import { ciTimeout, expect, test, type Page } from './test'

import { mockSessions } from './mocks'
import { terminalUrl } from './helpers'

// Copy pin: selecting terminal text must put it on the real system clipboard.
// The route-mocked WS keeps this hermetic (no Docker), but the selection,
// xterm's own selection model, the clipboard API, and readback are all real.
const FAKE_ID = 'e2e/copy-fake'

function binaryOutput(text: string): Buffer {
  return Buffer.concat([Buffer.from([0]), Buffer.from(text, 'utf8')])
}

async function terminalBox(page: Page) {
  const box = await page.locator('.xterm-screen').boundingBox()
  expect(box).toBeTruthy()
  return box!
}

// Drag across the terminal's first row: press its first cell, release at
// the box's left edge + dx (and optional y). tmux's mouse mode owns this
// gesture in prod; without it xterm builds its own selection.
async function dragSelect(page: Page, dx: number, toY?: number) {
  const box = await terminalBox(page)
  const y = box.y + 8
  await page.mouse.move(box.x + 1, y)
  await page.mouse.down()
  await page.mouse.move(box.x + dx, toY ?? y, { steps: 12 })
  await page.mouse.up()
}

// A phone cannot paint a selection like a mouse can: the pane is a canvas
// with `user-select: none`, so the app's own long-press → select-word →
// copy-on-lift path is the only one. Real TouchEvents (not touchscreen taps)
// drive it; `dragTo` is the fraction of the pane's width the finger slides
// to before lifting.
async function longPress(page: Page, dragTo?: number) {
  const box = await terminalBox(page)
  const x = box.x + 3
  const y = box.y + 8
  const to = dragTo === undefined ? x : box.x + box.width * dragTo
  await page.evaluate(async ({ x, y, to, drag }) => {
    const screen = document.querySelector('.xterm-screen') as HTMLElement
    const touch = (cx: number): Touch =>
      new Touch({ identifier: 9, target: screen, clientX: cx, clientY: y, screenX: cx, screenY: y })
    const fire = (type: string, cx: number, touches: Touch[]) =>
      screen.dispatchEvent(new TouchEvent(type, {
        touches,
        targetTouches: touches,
        changedTouches: [touch(cx)],
        bubbles: true,
        cancelable: true,
      }))
    fire('touchstart', x, [touch(x)])
    await new Promise((resolve) => setTimeout(resolve, 650))
    if (drag) fire('touchmove', to, [touch(to)])
    fire('touchend', to, [])
  }, { x, y, to, drag: dragTo !== undefined })
}

async function openCopyTerminal(
  page: Page,
  output = 'COPY-ME-12345\r\nsecond line\r\n',
  tmuxSelection?: string,
  marker = output.split('\r\n')[0],
) {
  await mockSessions(page)
  if (tmuxSelection !== undefined) {
    await page.route('**/api/projects/*/sessions/*/selection', (route) =>
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ text: tmuxSelection, fresh: true }),
      }))
  }
  await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
    ws.onMessage(() => ws.send(binaryOutput(output)))
  })
  await page.goto(terminalUrl(FAKE_ID, 'main'))
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(15_000) })
  await expect.poll(() => page.locator('.xterm-rows').innerText()).toContain(marker)
}

test.describe('terminal copy', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('highlighting terminal output copies it to the clipboard', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await openCopyTerminal(page)

    await dragSelect(page, 260)

    await expect(page.getByTestId('toast')).toHaveText('Copied', { timeout: ciTimeout(5_000) })
    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toContain('COPY-ME-12345')
  })

  test('copies tmux\'s buffer when mouse mode owns the drag', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    // tmux attach enables mouse tracking, so xterm reports the drag instead
    // of making a local selection; the server endpoint is the copy source.
    await openCopyTerminal(
      page,
      '\x1b[?1000h\x1b[?1002h\x1b[?1006hCOPY-ME-12345\r\nsecond line\r\n',
      'TMUX-SELECTION',
      'COPY-ME-12345',
    )

    await dragSelect(page, 260)

    await expect(page.getByTestId('toast')).toHaveText('Copied', { timeout: ciTimeout(5_000) })
    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toBe('TMUX-SELECTION')
  })

  test('releasing the drag outside the terminal still copies', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await openCopyTerminal(page)

    const box = await terminalBox(page)
    const keys = await page.getByTestId('accessory-keys').boundingBox()
    expect(keys).toBeTruthy()
    await dragSelect(page, keys!.x + keys!.width / 2 - box.x, keys!.y + keys!.height / 2)

    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toContain('COPY-ME-12345')
  })
})

// The touch path: long-press selects the word under the finger, and the lift
// copies it while the gesture's user activation is still alive.
test.describe('terminal copy (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test('long-press selects the word under the finger and copies it', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await openCopyTerminal(page)

    await longPress(page)

    await expect(page.getByTestId('toast')).toHaveText('Copied', { timeout: ciTimeout(5_000) })
    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toBe('COPY-ME-12345')
  })

  test('dragging after a long press extends the selection', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await openCopyTerminal(page, 'alpha beta gamma\r\ntail\r\n')

    await longPress(page, 0.5)

    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toBe('alpha beta gamma')
  })
})
