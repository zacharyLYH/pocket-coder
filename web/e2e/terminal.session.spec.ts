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

  test('session tabs show the current session selected', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    const tab = page.getByTestId('tab-session-main')
    await expect(tab).toBeVisible()
    await expect(tab).toContainText('main')
    await expect(tab).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByTestId('tab-new')).toBeVisible()
    // pinned views sit in the same strip, colour-coded apart from sessions
    await expect(page.getByTestId('tab-diff')).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-session-tabs.png')
  })

  test('new tab button opens dialog', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByTestId('tab-new').click()
    await expect(page.getByText('New Tab', { exact: true })).toBeVisible()
    await expect(page.getByPlaceholder(/Tab name/)).toBeVisible()
    await expect(page).toHaveScreenshot('terminal-new-tab-dialog.png')
  })

  test('new tab dialog shows only installed harnesses', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    await page.getByTestId('tab-new').click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await expect(runSelect).toBeVisible()
    // with no installs, only Shell should be listed — harnesses are installed from the home page
    const optionTexts = await runSelect.locator('option').allTextContents()
    expect(optionTexts).toContain('Shell (bash)')
    expect(optionTexts.some((t) => t.includes('OpenCode'))).toBeFalsy()
    await expect(page).toHaveScreenshot('terminal-dialog-with-harnesses.png')
  })

  test('after installing, the harness appears in the new-tab picker', async ({ page }) => {
    await createProjectAndOpenTerminal(page)

    // install opencode into the project from the home card
    await page.goto('/')
    const row = page.locator('div.flex.items-center.justify-between', { hasText: 'OpenCode' })
    await row.getByRole('button', { name: 'Install…' }).click()
    await page.getByRole('button', { name: /Install in 1 project/ }).click()
    await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 300_000 })
    await page.getByRole('button', { name: 'Terminal' }).click()
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })

    await page.getByTestId('tab-new').click()
    const dialog = page.getByRole('dialog')
    const runSelect = dialog.locator('select')
    await expect(runSelect).toBeVisible()
    const optionTexts = await runSelect.locator('option').allTextContents()
    expect(optionTexts.some((t) => t.includes('OpenCode'))).toBeTruthy()
    await expect(page).toHaveScreenshot('terminal-dialog-harness-selected.png')
  })
})

// ─── Session deletion (visual journey) ───────────────────────────────

test.describe('session deletion', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  // Deleting the ATTACHED session must not leave a dead screen: the app
  // lands on the next remaining session (the pane's ensure path recreates
  // it), and the deleted name disappears from the picker for good — unlike
  // kill, which keeps metadata for one-click relaunch.
  test('deleting the attached session lands on another session', async ({ page }) => {
    test.setTimeout(300_000)
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await createProjectAndOpenTerminal(page)

      // create a second tab so there is somewhere to land
      await page.getByTestId('tab-new').click()
      const dialog = page.getByRole('dialog')
      await page.getByPlaceholder(/Tab name/).fill('dev')
      await dialog.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(dialog).not.toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })

      // stage 1: the strip shows both session tabs
      await expect(page.getByTestId('tab-session-main')).toBeVisible()
      await expect(page.getByTestId('tab-session-dev')).toBeVisible()
      await expect(page.getByTestId('tab-session-dev')).toHaveAttribute('aria-selected', 'true')
      await expect(page).toHaveScreenshot('terminal-session-delete-before.png')

      // stage 2: trigger the delete flow on the attached session via ✕ close button
      await page.getByTestId('tab-close-dev').click()

      // stage 3: landed on the other session, live again
      await expect(page.getByTestId('tab-session-main')).toHaveAttribute('aria-selected', 'true')
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })

      // stage 4: the deleted session is gone from the strip for good
      await expect(page.getByTestId('tab-session-dev')).toHaveCount(0)
      await expect(page.getByTestId('tab-session-main')).toBeVisible()
      await expect(page).toHaveScreenshot('terminal-session-delete-after.png')

      // stage 5: refresh the page and ensure it doesn't resurrect (state.json metadata was deleted)
      await page.reload()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await expect(page.getByTestId('tab-session-dev')).toHaveCount(0)
      await expect(page.getByTestId('tab-session-main')).toBeVisible()
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})

// ─── Multiple sessions ────────────────────────────────────────────────

test.describe('multiple sessions', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('creating a shell session adds it to the tab strip', async ({ page }) => {
    test.setTimeout(300_000)
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await createProjectAndOpenTerminal(page)

      // create a second tab through the real dialog
      await page.getByTestId('tab-new').click()
      const dialog = page.getByRole('dialog')
      await page.getByPlaceholder(/Tab name/).fill('dev')
      await dialog.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(dialog).not.toBeVisible({ timeout: 15_000 })

      // the app switches to the new tab and both are listed in the strip
      await expect(page.getByTestId('tab-session-dev')).toHaveAttribute('aria-selected', 'true')
      await expect(page.getByTestId('tab-session-main')).toBeVisible()
      await expect(page.getByTestId('tab-session-dev')).toBeVisible()
      await expect(page).toHaveScreenshot('terminal-multi-session-tabs.png')

      // switching back to main reattaches for real — click the tab
      await page.getByTestId('tab-session-main').click()
      await expect(page.getByTestId('tab-session-main')).toHaveAttribute('aria-selected', 'true')
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
