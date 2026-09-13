import { expect, test } from '@playwright/test'
import { createProjectViaUI, deleteAllProjects, e2eRepo, e2eRepoID, engineUp, fetchEvents } from './helpers'

test.describe.configure({ mode: 'serial' })

// Full-stack user journeys: real Go server (booted by playwright.config as a
// webServer) + Vite dev proxy + real login. NOTHING is mocked — even the PIN
// comes from the console-mailer output (see e2e/helpers.ts).
//
// The rule that keeps these tests honest: no API shortcuts for state the UI
// can create itself. The first test once pre-created the tmux session via a
// raw POST and thereby masked that clicking Terminal was broken for real
// users — the UI path IS the test path now.

test('create a project in the UI, open its terminal, type', async ({ page }) => {
  await page.goto('/')

  if (!(await engineUp(page.request))) {
    test.skip(true, 'Docker engine unavailable — full-stack terminal test skipped')
    return
  }

  try {
    // --- clean slate: leftovers from earlier tests in this run would make
    // row-scoping below ambiguous ---
    await deleteAllProjects(page.request)

    // --- create through the REAL form, exactly as a user would ---
    // (createProjectViaUI resubmits if the Vite dev client drops its
    // websocket and reloads mid-submit; on a cold engine the first create
    // includes building pcoder-project — minutes, so budgets stay generous)
    await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))
    const terminalButtons = page.getByRole('button', { name: 'Terminal' })
    await expect(terminalButtons).toHaveCount(1)

    // --- open the terminal: this must work with NO session ever created ---
    await terminalButtons.click()
    await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })

    await page.keyboard.type('echo journey7\n')
    await expect
      .poll(async () => page.locator('.xterm-rows').innerText(), { timeout: 15_000 })
      .toContain('journey7')

    // visual screenshots — desktop and phone variants of the same terminal
    await expect(page).toHaveScreenshot('terminal-desktop.png', { caret: 'hide' })
    await page.setViewportSize({ width: 390, height: 844 })
    await page.waitForTimeout(500)
    await expect(page).toHaveScreenshot('terminal-phone.png', { caret: 'hide' })
    await page.setViewportSize({ width: 1280, height: 720 })

    // plumbing proof in the backend's own audit trail: clicking Terminal
    // created the session AND attached to it — no API help needed. Read via
    // the API: the compose server runs as root, so on Linux CI the log file
    // on the bind mount is root-owned 0600 (EACCES for the runner user).
    const types = (await fetchEvents(page.request)).map((e) => e.type)
    expect(types).toContain('session.create')
    expect(types).toContain('terminal.attach')
  } finally {
    // destructor: never leak projects/volumes, even on failure
    await deleteAllProjects(page.request)
  }
})

// This is intentionally not a mocked visual test. OpenCode is a full-screen
// TUI whose Unicode glyphs, ANSI colors, tmux negotiation, and layout cannot
// be represented by a scripted WebSocket. Keep the screenshot as a test
// artifact so a real OpenCode rendering can be inspected or compared against
// the expected UI.
test('real OpenCode session renders through the backend terminal bridge', async ({ page }, testInfo) => {
  test.setTimeout(600_000)
  await page.goto('/')

  if (!(await engineUp(page.request))) {
    test.skip(true, 'Docker engine unavailable — full-stack terminal test skipped')
    return
  }

  try {
    await deleteAllProjects(page.request)
    await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))

    // installs are explicit and per project: install OpenCode through the
    // home page (real npm download) BEFORE launching it
    const row = page.locator('div.flex.items-center.justify-between', { hasText: 'OpenCode' })
    await row.getByRole('button', { name: 'Install…' }).click()
    await page.getByRole('button', { name: /Install in 1 project/ }).click()
    await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 300_000 })

    await page.getByRole('button', { name: 'Terminal' }).click()
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })

    await page.getByTestId('tab-new').click()
    const dialog = page.getByRole('dialog')
    await dialog.getByPlaceholder(/Tab name/).fill('opencode-1')
    await dialog.locator('select').selectOption('opencode')
    await dialog.getByRole('button', { name: 'Create & Attach' }).click()

    await expect(dialog).not.toBeVisible({ timeout: 240_000 })
    const opencodeTab = page.getByTestId('tab-session-opencode-1')
    await expect(opencodeTab).toContainText('opencode-1', { timeout: 30_000 })
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 30_000 })
    await expect(page.locator('.xterm-screen')).toBeVisible()

    // Exercise the same tab interaction as the reported naming bug,
    // then save the real rendered OpenCode screen for visual review.
    await expect(page.getByTestId('tab-session-main')).toBeVisible()
    await page.getByTestId('tab-session-main').click()
    await expect(page.getByTestId('tab-session-main')).toHaveAttribute('aria-selected', 'true')
    await opencodeTab.click()
    await expect(opencodeTab).toHaveAttribute('aria-selected', 'true')
    await expect(page.getByText('Connected')).toBeVisible({ timeout: 30_000 })
    await expect
      .poll(async () => page.locator('.xterm-rows').innerText(), { timeout: 60_000 })
      .toMatch(/Build|Connect|Ask anything/)
    await page.waitForTimeout(2_000)
    await page.screenshot({ path: testInfo.outputPath('opencode-real-backend.png'), fullPage: true })
    await expect(page).toHaveScreenshot('terminal-opencode-stack.png', {
      animations: 'disabled',
      caret: 'hide',
    })
    // mobile variant was missing — capture the same real render on a phone viewport
    await page.setViewportSize({ width: 390, height: 844 })
    await page.waitForTimeout(500)
    await expect(page).toHaveScreenshot('terminal-opencode-stack-mobile.png', {
      animations: 'disabled',
      caret: 'hide',
    })
    await page.setViewportSize({ width: 1280, height: 720 })

    const terminalText = await page.locator('.xterm-rows').innerText()
    expect(terminalText).toMatch(/Build|Connect|Ask anything/)
  } finally {
    await deleteAllProjects(page.request)
  }
})
