import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'

// Visual tests for the terminal, rendered by a real xterm.js attached to a
// real tmux session in a real project container. The prompt line carries the
// container's random hostname — a tiny, per-run-varying region absorbed by
// the global maxDiffPixelRatio; everything else (layout, chrome, colors)
// must match. Phone is covered deliberately: it is the primary use case.

async function openTerminal(page: import('@playwright/test').Page) {
  await page.goto('/')
  await page.getByRole('button', { name: 'Terminal' }).click()
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
}

async function typeUntilRendered(page: import('@playwright/test').Page) {
  await page.keyboard.type('echo hello')
  await expect
    .poll(async () => {
      const text = await page.locator('.xterm-rows').innerText()
      // xterm rows may break mid-word; strip row-newlines to get logical text
      return text.replace(/\n/g, '')
    }, { timeout: 15_000 })
    .toContain('echo hello')
}

test.describe('desktop', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('terminal renders on desktop', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await page.goto('/')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 60_000 })

      await openTerminal(page)
      await typeUntilRendered(page)

      await expect(page).toHaveScreenshot('terminal-desktop.png', { caret: 'hide' })

      // a real `exit` ends the shell, the session, and the attach
      await page.keyboard.press('Enter')
      await page.keyboard.type('exit')
      await page.keyboard.press('Enter')
      await expect(page.getByText('Disconnected')).toBeVisible({ timeout: 10_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})

test.describe('phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('terminal renders on a phone', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await page.goto('/')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 60_000 })

      await openTerminal(page)
      await typeUntilRendered(page)

      await expect(page).toHaveScreenshot('terminal-phone.png', { caret: 'hide' })
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
