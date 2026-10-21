import { ciTimeout, expect, test } from './test'

import { mockSessions } from './mocks'
import { terminalUrl } from './helpers'

// Copy pin: selecting terminal text must put it on the real system clipboard.
// The route-mocked WS keeps this hermetic (no Docker), but the selection,
// xterm's own selection model, the clipboard API, and readback are all real.
const FAKE_ID = 'e2e/copy-fake'

function binaryOutput(text: string): Buffer {
  return Buffer.concat([Buffer.from([0]), Buffer.from(text, 'utf8')])
}

test.describe('terminal copy', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('highlighting terminal output copies it to the clipboard', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await mockSessions(page)
    await page.routeWebSocket(/\/ws\/projects\//, (ws) => {
      ws.onMessage(() => ws.send(binaryOutput('COPY-ME-12345\r\nsecond line\r\n')))
    })

    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(15_000) })
    await expect.poll(() => page.locator('.xterm-rows').innerText()).toContain('COPY-ME-12345')

    const box = await page.locator('.xterm-screen').boundingBox()
    expect(box).toBeTruthy()
    const y = box!.y + 8
    await page.mouse.move(box!.x + 6, y)
    await page.mouse.down()
    await page.mouse.move(box!.x + 260, y, { steps: 12 })
    await page.mouse.up()

    await expect.poll(
      () => page.evaluate(() => navigator.clipboard.readText()),
      { timeout: ciTimeout(5_000) },
    ).toContain('COPY-ME-12345')
  })
})
