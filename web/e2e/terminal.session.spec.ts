import { expect, test, type Page } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'

// Tests for the terminal session management UI — session dropdown, new
// session dialog, session switching — against the real backend: real tmux
// sessions, real harness registry, real WebSocket.

async function createProjectAndOpenTerminal(page: Page) {
  await page.goto('/')
  await page.getByRole('button', { name: 'Create project' }).click()
  await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 300_000 })
  await page.getByRole('button', { name: 'Terminal' }).click()
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
  await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
}

test.describe('session switcher', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('session dropdown shows current session', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    const sessionButton = page.getByRole('button', { name: 'Session', exact: true })
    await expect(sessionButton).toBeVisible()
    await expect(sessionButton).toContainText('main')
    await sessionButton.click()
    await expect(page.getByRole('menu')).toBeVisible()
    await expect(page.getByRole('menuitem', { name: 'main' })).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-session-dropdown.png')
    await page.keyboard.press('Escape')
  })

  test('new session button opens dialog', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByRole('button', { name: '+ New Session' }).click()
    await expect(page.getByText('New Session', { exact: true })).toBeVisible()
    await expect(page.getByPlaceholder(/Session name/)).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-new-session-dialog.png')
  })

  test('new session dialog shows only installed harnesses', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByRole('button', { name: '+ New Session' }).click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await expect(runSelect).toBeVisible()
    // with no installs, only Shell should be listed — harnesses are installed from the home page
    const optionTexts = await runSelect.locator('option').allTextContents()
    expect(optionTexts).toContain('Shell (bash)')
    expect(optionTexts.some((t) => t.includes('OpenCode'))).toBeFalsy()
    await expect(page).toHaveScreenshot('terminal-dialog-with-harnesses.png')
  })

  test('after installing, the harness appears in the new-session picker', async ({ page }) => {
    await page.goto('/')
    await deleteAllProjects(page.request)
    await page.getByRole('button', { name: 'Create project' }).click()
    await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 300_000 })
    // install opencode into the new project from the home card
    const row = page.locator('div.flex.items-center.justify-between', { hasText: 'OpenCode' })
    await row.getByRole('button', { name: 'Install…' }).click()
    await page.getByRole('button', { name: /Install in 1 project/ }).click()
    await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 300_000 })
    await page.getByRole('button', { name: 'Terminal' }).click()
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })

    await page.getByRole('button', { name: '+ New Session' }).click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await expect(runSelect).toBeVisible()
    const optionTexts = await runSelect.locator('option').allTextContents()
    expect(optionTexts.some((t) => t.includes('OpenCode'))).toBeTruthy()
    await expect(page).toHaveScreenshot('terminal-dialog-harness-selected.png')
  })
})

// ─── Multiple sessions ────────────────────────────────────────────────

test.describe('multiple sessions', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('creating a shell session adds it to the dropdown', async ({ page }) => {
    test.setTimeout(300_000)
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await createProjectAndOpenTerminal(page)

      // create a second (shell) session through the real dialog
      await page.getByRole('button', { name: '+ New Session' }).click()
      const dialog = page.getByRole('dialog')
      await page.getByPlaceholder(/Session name/).fill('dev')
      await dialog.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(dialog).not.toBeVisible({ timeout: 15_000 })

      // the app switches to the new session and both are listed — open the picker to prove it
      const sessionButton = page.getByRole('button', { name: 'Session', exact: true })
      await expect(sessionButton).toContainText('dev')
      await sessionButton.click()
      await expect(page.getByRole('menu')).toBeVisible()
      const optionTexts = await page.getByRole('menuitem').allTextContents()
      expect(optionTexts).toContain('main')
      expect(optionTexts).toContain('dev')
      await expect(page).toHaveScreenshot('terminal-multi-session-dropdown.png')

      // switching back to main reattaches for real — click the option while the picker is open
      await page.getByRole('menuitem', { name: 'main' }).click({ timeout: 10_000 })
      await expect(sessionButton).toContainText('main')
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
