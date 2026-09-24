import { expect, test } from '@playwright/test'

import { mockGit, resetGitState, terminalUrl, PROJECT } from './git.fixtures'

// Mobile Git tab (390x844): route-mocked, no engine. Review → context
// expansion → commit → push → AI description, plus push-failure,
// branch switching, and quick keys. Follows the nerdy.mobile.spec.ts
// pattern: mock the API, drive the real TerminalView.
async function openGitTab(page: import('@playwright/test').Page, opts?: Parameters<typeof mockGit>[1]) {
  resetGitState()
  await mockGit(page, opts)
  await page.goto(terminalUrl(PROJECT, 'main'))
  await page.getByTestId('tab-diff').click()
  await expect(page.getByTestId('diff-tab')).toBeVisible({ timeout: 10_000 })
}

test.describe('git tab (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(async ({ context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
  })

  test('review → expand context → commit → push → draft description', async ({ page }) => {
    await openGitTab(page)
    // Message box sits at the top with Generate.
    await expect(page.getByTestId('git-message')).toBeVisible()
    await expect(page.getByTestId('git-generate-message')).toBeVisible()

    // Expand the file: hunks render at 3 context lines…
    await page.getByTestId('diff-file').click()
    await expect(page.getByTestId('diff-stage-hunk').first()).toBeVisible()
    await expect(page.getByTestId('diff-context-bump')).toContainText('Context: 3')

    // …raise to 10 and collapse back.
    await page.getByTestId('diff-context-bump').click()
    await expect(page.getByTestId('diff-context-bump')).toContainText('Context: 10')
    await page.getByTestId('git-collapse-all').click()
    await expect(page.getByTestId('diff-stage-hunk')).toBeHidden()

    // Commit lands locally: sha chip, clean tree, push-only retry state.
    await page.getByTestId('diff-file').click()
    await page.getByTestId('git-message').fill('feat: add world greeting')
    await page.getByTestId('git-commit').click()
    await expect(page.getByTestId('git-commit-sha')).toContainText('abc1234')
    await expect(page.getByTestId('diff-empty')).toContainText('No changes')
    await expect(page.getByTestId('git-commit')).toBeDisabled()
    await expect(page.getByTestId('git-push')).toBeEnabled()

    // Push ships it, then the AI description is copy-only.
    await page.getByTestId('git-push').click()
    await expect(page.getByTestId('git-push-state')).toContainText('Pushed ✓')
    await expect(page.getByTestId('diff-tab')).toHaveScreenshot('git-mobile-shipped.png', { timeout: 20_000 })

    await page.getByTestId('git-draft-desc').click()
    await expect(page.getByTestId('git-pr-desc')).toContainText('Add world greeting')
    await page.getByTestId('git-pr-desc-copy').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('Add world greeting')
    await expect(page.getByTestId('git-ship')).toHaveScreenshot('git-mobile-desc.png')
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

  test('push is disabled on a repo with no commits yet', async ({ page }) => {
    await openGitTab(page, { unborn: true })
    await expect(page.getByTestId('git-push')).toBeDisabled()
    await expect(page.getByTestId('git-push-hint')).toContainText('Commit first')
  })

  test('working-tree explain is disabled on a clean tree', async ({ page }) => {
    await openGitTab(page)
    await page.getByTestId('diff-file').click()
    await expect(page.getByTestId('git-explain-tree')).toBeEnabled()
    await page.getByTestId('git-message').fill('feat: add world greeting')
    await page.getByTestId('git-commit').click()
    await expect(page.getByTestId('git-explain-tree')).toBeDisabled()
  })

  test('push failure keeps the sha and shows a copyable error', async ({ page }) => {
    await openGitTab(page, {
      pushFail: 'To github.com:demo/git.git\n ! [rejected] main -> main (non-fast-forward)\nerror: failed to push some refs — fix credentials or conflicts in the terminal',
    })

    await page.getByTestId('git-message').fill('feat: add world greeting')
    await page.getByTestId('git-commit').click()
    await expect(page.getByTestId('git-commit-sha')).toContainText('abc1234')

    // Partial success is honest: commit landed, push failed, retry is
    // push-only with no duplicate commit.
    await page.getByTestId('git-push').click()
    await expect(page.getByTestId('git-push-state')).toContainText('Push rejected ✗')
    await expect(page.getByTestId('git-error')).toContainText('non-fast-forward')
    await expect(page.getByTestId('git-commit')).toBeDisabled()
    await expect(page.getByTestId('git-push')).toBeEnabled()

    // The error copies verbatim for pasting into their AI.
    await page.getByTestId('git-error-copy').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toContain('non-fast-forward')
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
    await expect(dialog.getByTestId('sc-run-Esc')).toBeVisible()
    await expect(dialog.getByTestId('sc-run-Ctrl-C')).toBeVisible()
    await expect(dialog).toHaveScreenshot('git-mobile-shortcuts.png')

    // running a key sends terminal input without any network: the modal closes
    await dialog.getByTestId('sc-run-Ctrl-C').click()
    await expect(dialog).not.toBeVisible()
  })
})
