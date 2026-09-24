import { expect, test, type Page } from '@playwright/test'
import { createProjectViaUI, deleteAllProjects, e2eRepo, e2eRepoID, engineUp } from './helpers'

// Mobile typing is the whole requirement: tap the terminal on a phone
// viewport and typed text reaches the session. No copy/paste, no accessory
// bar, no higher-order tooling — just proof that a phone user can drive a
// shell and a real opencode TUI through the existing WS bridge.

async function openShellTerminal(page: Page) {
  await page.goto('/')
  await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))
  await page.getByRole('button', { name: 'Terminal' }).click()
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
}

async function terminalText(page: Page): Promise<string> {
  return page.locator('.xterm-rows').innerText()
}

test.describe('mobile typing (shell)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('tap focuses and typed text reaches the shell', async ({ page }) => {
    await openShellTerminal(page)
    await page.locator('.xterm-screen').tap()
    await page.keyboard.type('echo mobile-type-7\n')
    await expect.poll(() => terminalText(page), { timeout: 15_000 }).toContain('mobile-type-7')
  })
})

test.describe('mobile typing (opencode TUI)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('tap focuses and typed text reaches the opencode prompt', async ({ page }) => {
    test.setTimeout(600_000)
    await page.goto('/')
    const id = await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))

    await page.getByTestId(`project-menu-${id}`).click()
    await page.getByRole('menuitem', { name: /Harnesses/ }).click()
    const hdialog = page.getByRole('dialog')
    const row = hdialog.locator('div.flex.items-center.justify-between', { hasText: 'OpenCode' })
    await row.getByRole('button', { name: 'Install…' }).click()
    await hdialog.getByRole('button', { name: /Install in 1 project/ }).click()
    await expect(hdialog.getByText('Applied to 1 project.')).toBeVisible({ timeout: 300_000 })
    await page.keyboard.press('Escape')
    await expect(hdialog).not.toBeVisible({ timeout: 5_000 })

    await page.getByRole('button', { name: 'Terminal' }).click()
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })
    await page.getByTestId('tab-new').click()
    const dialog = page.getByRole('dialog')
    await dialog.getByPlaceholder(/Tab name/).fill('opencode-mobile')
    await dialog.locator('select').selectOption('opencode')
    await dialog.getByRole('button', { name: 'Create & Attach' }).click()
    await expect(dialog).not.toBeVisible({ timeout: 240_000 })
    await expect.poll(() => terminalText(page), { timeout: 60_000 }).toMatch(/Build|Connect|Ask anything/)

    // The TUI prompt is live: typed text lands in its input, visible on screen.
    await page.locator('.xterm-screen').tap()
    await page.keyboard.type('hello mobile')
    await expect.poll(() => terminalText(page), { timeout: 15_000 }).toContain('hello mobile')
  })
})
