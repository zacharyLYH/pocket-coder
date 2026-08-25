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

    const sessionSelect = page.locator('select[aria-label="Session"]')
    await expect(sessionSelect).toBeVisible()
    await expect(sessionSelect).toHaveValue('main')
    await expect(page).toHaveScreenshot('terminal-session-dropdown.png')
  })

  test('new session button opens dialog', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByRole('button', { name: '+ New Session' }).click()
    await expect(page.getByText('New Session', { exact: true })).toBeVisible()
    await expect(page.getByPlaceholder(/Session name/)).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-new-session-dialog.png')
  })

  test('new session dialog shows harness options', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByRole('button', { name: '+ New Session' }).click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await expect(runSelect).toBeVisible()
    // the real harness registry lists OpenCode (options are hidden inside select)
    const optionTexts = await runSelect.locator('option').allTextContents()
    expect(optionTexts.some((t) => t.includes('OpenCode'))).toBeTruthy()
    await expect(page).toHaveScreenshot('terminal-dialog-with-harnesses.png')
  })

  test('selecting an uninstalled harness shows how to get it', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByRole('button', { name: '+ New Session' }).click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await runSelect.selectOption('opencode')
    // installs are explicit and per project: the dialog says where to do it
    await expect(page.getByText(/Not installed in this project yet/)).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-dialog-harness-selected.png')
  })
})

// ─── Multiple sessions ────────────────────────────────────────────────

test.describe('multiple sessions', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('creating a shell session adds it to the dropdown', async ({ page }) => {
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

      // the app switches to the new session and both are listed
      const sessionSelect = page.locator('select[aria-label="Session"]')
      await expect(sessionSelect).toHaveValue('dev', { timeout: 10_000 })
      const optionTexts = await sessionSelect.locator('option').allTextContents()
      expect(optionTexts).toContain('main')
      expect(optionTexts).toContain('dev')

      // switching back to main reattaches for real
      await sessionSelect.selectOption('main')
      await expect(sessionSelect).toHaveValue('main')
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
