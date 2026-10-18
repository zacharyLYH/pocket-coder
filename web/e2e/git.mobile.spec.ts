import { ciTimeout, expect, test } from './test'

import { mockGit, resetGitState, PROJECT } from './git.fixtures'
import { terminalUrl } from './helpers'

// Mobile Git tab (390x844): route-mocked, no engine. Review → context
// expansion → commit → push → AI description, plus push-failure,
// branch switching, and quick keys. Follows the nerdy.mobile.spec.ts
// pattern: mock the API, drive the real TerminalView.
async function openGitTab(page: import('@playwright/test').Page, opts?: Parameters<typeof mockGit>[1]) {
  resetGitState()
  await mockGit(page, opts)
  await page.goto(terminalUrl(PROJECT, 'main'))
  await page.getByTestId('tab-diff').click()
  await expect(page.getByTestId('diff-tab')).toBeVisible({ timeout: ciTimeout(10_000) })
}

test.describe('git tab (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(async ({ context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  })

  test('review → expand context → commit → push → draft description', async ({ page }) => {
    await openGitTab(page)
    // Message box lives in the sticky action card with Generate.
    await expect(page.getByTestId('git-message')).toBeVisible()
    await expect(page.getByTestId('git-generate-message')).toBeVisible()

    // Stage is available without opening the file, and the row shows a
    // pointer cursor because the whole label area is the dropdown trigger.
    const toggle = page.getByTestId('diff-file-toggle')
    await expect(toggle).toHaveClass(/cursor-pointer/)
    await expect(page.getByTestId('diff-stage-file')).toBeVisible()

    // Expand the file: hunks render at 3 context lines…
    await toggle.click()
    await expect(page.getByTestId('diff-stage-hunk').first()).toBeVisible()
    await expect(page.getByTestId('diff-context-bump')).toContainText('Context: 3')

    // …raise to 10 and collapse back.
    await page.getByTestId('diff-context-bump').click()
    await expect(page.getByTestId('diff-context-bump')).toContainText('Context: 10')
    await page.getByTestId('git-collapse-all').click()
    await expect(page.getByTestId('diff-stage-hunk')).toBeHidden()
    await expect(page.getByTestId('diff-stage-file')).toBeVisible()

    // One CTA, label changes with state: Stage all → Commit → Push.
    await expect(page.getByTestId('git-primary-action')).toHaveText('Stage all')
    await page.getByTestId('git-primary-action').click()
    await expect(page.getByTestId('git-primary-action')).toHaveText('Commit')
    await expect(page.getByTestId('git-primary-action')).toBeDisabled()
    await expect(page.getByTestId('git-primary-hint')).toContainText('Enter a commit message')
    await expect(page.getByTestId('git-actions')).toHaveScreenshot('git-mobile-actions-commit.png')

    // Commit lands locally: sha chip, clean tree, push-only retry state.
    await page.getByTestId('git-message').fill('feat: add world greeting')
    await expect(page.getByTestId('git-primary-action')).toBeEnabled()
    await page.getByTestId('git-primary-action').click()
    await expect(page.getByTestId('git-commit-sha')).toContainText('abc1234')
    await expect(page.getByTestId('diff-empty')).toContainText('No changes')
    await expect(page.getByTestId('git-primary-action')).toHaveText('Push')

    // Push ships it, then the AI description is copy-only.
    await page.getByTestId('git-primary-action').click()
    await expect(page.getByTestId('git-push-state')).toContainText('Pushed ✓')
    await expect(page.getByTestId('diff-tab')).toHaveScreenshot('git-mobile-shipped.png', { timeout: ciTimeout(20_000) })

    await page.getByTestId('git-draft-desc').click()
    await expect(page.getByTestId('git-pr-desc')).toContainText('Add world greeting')
    await page.getByTestId('git-pr-desc-copy').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('Add world greeting')
    await expect(page.getByTestId('git-ship')).toHaveScreenshot('git-mobile-desc.png')
  })

  // Pins the Quote button's whole job: expand a file, open the comment
  // dialog, add a comment, and land path + patch + comment in the notes
  // without an uncaught error. The pageerror listener is the point: a
  // thrown exception still leaves the UI usable, so value-only assertions
  // would miss it.
  test('quote opens the comment dialog and appends to notes without throwing', async ({ page }) => {
    const pageErrors: string[] = []
    page.on('pageerror', (e) => pageErrors.push(e.message))
    await openGitTab(page)

    const listBox = await page.getByTestId('diff-file-list').boundingBox()
    const actionsBox = await page.getByTestId('git-actions').boundingBox()
    const viewport = page.viewportSize()
    expect(listBox && actionsBox && viewport).toBeTruthy()
    expect(listBox!.height).toBeGreaterThanOrEqual(viewport!.height / 2)
    expect(actionsBox!.y).toBeGreaterThanOrEqual(listBox!.y + listBox!.height)

    await page.getByTestId('diff-file-toggle').click()
    await page.getByTestId('diff-quote-hunk').first().click()

    const dialog = page.getByTestId('quote-dialog')
    await expect(dialog).toBeVisible()
    await expect(dialog).toContainText('hello.txt')
    await page.getByTestId('quote-comment').fill('Is this value sanitized?')
    await page.getByTestId('quote-add').click()

    const notes = page.getByTestId('diff-notes')
    await expect(dialog).toBeHidden()
    await expect(notes).toBeVisible()
    await expect(notes).toHaveValue(/hello\.txt/)
    await expect(notes).toHaveValue(/\+world/)
    await expect(notes).toHaveValue(/Is this value sanitized\?/)

    // The point of the layout: commit and notes split the deck side by side.
    const commitBox = await page.getByTestId('git-commit-section').boundingBox()
    const notesBox = await page.getByTestId('diff-notepad').boundingBox()
    expect(commitBox && notesBox).toBeTruthy()
    expect(Math.abs(commitBox!.width - notesBox!.width)).toBeLessThan(2)
    expect(commitBox!.x).toBeLessThan(notesBox!.x)
    expect(pageErrors).toEqual([])
  })

  test('generate fills the message; explain jumps to codemap', async ({ page }) => {
    await openGitTab(page)
    await page.getByTestId('git-generate-message').click()
    await expect(page.getByTestId('git-message')).toHaveValue(/feat: add world greeting/)

    // Explain covers the dirty working tree (pre-commit path) and opens Codemap.
    await expect(page.getByTestId('git-explain-tree')).toBeEnabled()
    await page.getByTestId('git-explain-tree').click()
    await expect(page.getByTestId('tab-codemap')).toHaveAttribute('aria-selected', 'true')
  })

  test('unborn repo shows Commit first, not Push', async ({ page }) => {
    await openGitTab(page, { unborn: true })
    await expect(page.getByTestId('git-primary-action')).toHaveText('Commit first')
    await expect(page.getByTestId('git-primary-action')).toBeDisabled()
    await expect(page.getByTestId('git-primary-hint')).toContainText('no commits yet')
  })

  test('working-tree explain is disabled on a clean tree', async ({ page }) => {
    await openGitTab(page)
    await page.getByTestId('diff-file-toggle').click()
    await expect(page.getByTestId('git-explain-tree')).toBeEnabled()
    await page.getByTestId('git-primary-action').click() // Stage all
    await page.getByTestId('git-message').fill('feat: add world greeting')
    await page.getByTestId('git-primary-action').click() // Commit
    await expect(page.getByTestId('git-explain-tree')).toBeDisabled()
  })

  test('push failure keeps the sha and shows a copyable error', async ({ page }) => {
    await openGitTab(page, {
      pushFail: 'To github.com:demo/git.git\n ! [rejected] main -> main (non-fast-forward)\nerror: failed to push some refs — fix credentials or conflicts in the terminal',
    })

    await page.getByTestId('git-primary-action').click() // Stage all
    await page.getByTestId('git-message').fill('feat: add world greeting')
    await page.getByTestId('git-primary-action').click() // Commit
    await expect(page.getByTestId('git-commit-sha')).toContainText('abc1234')

    // Partial success is honest: commit landed, push failed, retry is
    // push-only with no duplicate commit.
    await expect(page.getByTestId('git-primary-action')).toHaveText('Push')
    await page.getByTestId('git-primary-action').click()
    await expect(page.getByTestId('git-push-state')).toContainText('Push rejected ✗')
    await expect(page.getByTestId('git-error')).toContainText('non-fast-forward')
    await expect(page.getByTestId('git-primary-action')).toHaveText('Push')

    // The error copies verbatim for pasting into their AI.
    await page.getByTestId('git-error-copy').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('non-fast-forward')
  })

  test('ahead branch folds commit and push into one CTA', async ({ page }) => {
    await openGitTab(page, { ahead: 1 })

    // Unstaged work outranks the pending push: stage it first.
    await expect(page.getByTestId('git-primary-action')).toHaveText('Stage all')
    await page.getByTestId('git-primary-action').click()

    await page.getByTestId('git-message').fill('feat: add world greeting')
    await expect(page.getByTestId('git-primary-action')).toHaveText('Commit & Push')
    await page.getByTestId('git-primary-action').click()

    // One press lands the commit and ships it: no second button hunt.
    await expect(page.getByTestId('git-commit-sha')).toContainText('abc1234')
    await expect(page.getByTestId('git-push-state')).toContainText('Pushed ✓')
    await expect(page.getByTestId('git-primary-action')).toHaveText('Up to date')
  })

  test('unresolved merge conflict blocks the CTA and badges the file', async ({ page }) => {
    await openGitTab(page, { conflict: true })

    // Detection replaces the failure surprise: the CTA names the blocker.
    await expect(page.getByTestId('git-primary-action')).toHaveText('Resolve conflicts')
    await expect(page.getByTestId('git-primary-action')).toBeDisabled()
    await expect(page.getByTestId('git-primary-hint')).toContainText('merge in progress')

    // Conflicts sort above the normal sections and never offer staging.
    await expect(page.getByTestId('diff-conflicts')).toContainText('Conflicts (1)')
    await expect(page.getByTestId('diff-conflict-badge')).toHaveCount(1)
    await page.getByTestId('diff-conflicts').getByTestId('diff-file-toggle').click()
    await expect(page.getByTestId('diff-conflicts').getByTestId('diff-stage-file')).toHaveCount(0)
    await expect(page.getByTestId('diff-tab')).toHaveScreenshot('git-mobile-conflict.png')
  })
})

test.describe('git branches (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test('branch sheet → switch → badge', async ({ page }) => {
    resetGitState()
    await mockGit(page)
    await page.goto(terminalUrl(PROJECT, 'main'))
    await page.getByTestId('tab-diff').click()
    await expect(page.getByTestId('git-branch-badge')).toContainText('main')

    await page.getByTestId('git-branch-badge').click()
    await expect(page.getByTestId('git-branch-sheet')).toBeVisible()
    await expect(page.getByTestId('git-branch-sheet')).toHaveScreenshot('git-mobile-branches.png')

    await page.getByTestId('git-branch-local').filter({ hasText: 'feature/other' }).click()
    await expect(page.getByTestId('git-branch-sheet')).toBeHidden()
  })

  test('dirty-tree switch surfaces the 409 with the file list', async ({ page }) => {
    resetGitState()
    await mockGit(page, { switchConflict: true })
    await page.goto(terminalUrl(PROJECT, 'main'))
    await page.getByTestId('tab-diff').click()
    await page.getByTestId('git-branch-badge').click()
    await page.getByTestId('git-branch-local').filter({ hasText: 'feature/other' }).click()
    await expect(page.getByTestId('git-error')).toContainText('hello.txt')
  })
})

test.describe('terminal keys (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test('keys live in the shortcuts modal', async ({ page }) => {
    resetGitState()
    await mockGit(page)
    await page.route('**/api/projects/demo%2Fgit', (r) =>
      r.fulfill({
        json: {
          shortcuts: [
            { id: 'default-esc', alias: 'Esc', kind: 'keys', keys: 'Esc' },
            { id: 'default-ctrl-c', alias: 'Ctrl-C', kind: 'keys', keys: 'Ctrl-C' },
          ],
        },
      }))
    await page.goto(terminalUrl(PROJECT, 'main'))
    await page.getByTestId('tab-shortcuts').click()
    const dialog = page.getByRole('dialog')
    // Row tap runs: no separate play button on the terminal modal.
    await expect(dialog.getByTestId('sc-row-Esc')).toBeVisible()
    await expect(dialog.getByTestId('sc-row-Ctrl-C')).toBeVisible()
    await expect(dialog).toHaveScreenshot('git-mobile-shortcuts.png')

    // running a key sends terminal input without any network: the modal closes
    await dialog.getByTestId('sc-row-Ctrl-C').click()
    await expect(dialog).not.toBeVisible()
  })
})
