import { expect, test } from '@playwright/test'
import { createProject, deleteAllProjects, engineUp, projectURL, waitForRunning } from './helpers'

// Visual + behavioral tests for the Diff tab against the real backend:
// file list, per-file diff, hunk staging, and the copy-only Ask-AI notepad.
//
// Diffs are seeded through /api/projects/exec, which runs shell in the
// project container — the same path a harness session would take.

// Seed the cloned repo (/workspace/repo) with one modified tracked file
// and one untracked file. The fixture clone ships hello.txt committed.
async function seedDirtyRepo(request: import('@playwright/test').APIRequestContext, id: string) {
  const command = [
    'cd /workspace/repo',
    'git config user.email t@t.t',
    'git config user.name t',
    'echo world >> hello.txt',
    'echo new > untracked.txt',
  ].join(' && ')
  const res = await request.post('/api/projects/exec', { data: { projectIds: [id], command } })
  expect(res.ok()).toBeTruthy()
}

async function openDiffTab(
  page: import('@playwright/test').Page,
  request: import('@playwright/test').APIRequestContext,
  seed: boolean,
): Promise<string> {
  const id = await createProject(request)
  await waitForRunning(request, id)
  if (seed) await seedDirtyRepo(request, id)
  await page.goto(`/projects/${projectURL(id)}/terminal/main`)
  await page.getByTestId('tab-diff').click()
  await expect(page.getByTestId('diff-tab')).toBeVisible({ timeout: 30_000 })
  return id
}

// Screenshots scope to the diff tab element: the terminal header carries
// live connection status, which would flake full-page baselines.
async function shotDiffTab(page: import('@playwright/test').Page, name: string) {
  await expect(page.getByTestId('diff-tab')).toHaveScreenshot(name)
}

test.describe('diff tab empty', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('clean tree shows the empty state', async ({ page, request }) => {
    const id = await createProject(request)
    await waitForRunning(request, id)
    try {
      // a fresh clone is a clean tree — no seeding needed
      await page.goto(`/projects/${projectURL(id)}/terminal/main`)
      await page.getByTestId('tab-diff').click()
      await expect(page.getByTestId('diff-empty')).toBeVisible({ timeout: 30_000 })
      await expect(page.getByText('No changes')).toBeVisible()
      await shotDiffTab(page, 'diff-empty.png')
    } finally {
      await deleteAllProjects(request)
    }
  })
})

test.describe('diff tab with changes', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('file list, expanded diff, quote, and stage flow', async ({ page, request }) => {
    test.setTimeout(300_000)
    const id = await openDiffTab(page, request, true)
    try {
      // File list shows both the modified and the untracked file.
      await expect(page.getByTestId('diff-file-list')).toBeVisible({ timeout: 30_000 })
      await shotDiffTab(page, 'diff-file-list.png')
      await expect(page.getByText('hello.txt')).toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('untracked.txt')).toBeVisible({ timeout: 15_000 })

      // Expand the modified file: hunks render with per-hunk actions. The
      // viewer builds row structure before filling line text, so wait for
      // the added line's text (not just the row boxes) before screenshotting.
      await page.getByText('hello.txt').click()
      await expect(page.getByTestId('diff-stage-hunk').first()).toBeVisible({ timeout: 30_000 })
      await expect(
        page.getByTestId('diff-file').locator('.diff-table-body').getByText('world').first(),
      ).toBeVisible({ timeout: 30_000 })
      await shotDiffTab(page, 'diff-file-open.png')

      // Quote a hunk into the notepad.
      await page.getByTestId('diff-quote-hunk').first().click()
      await expect(page.getByTestId('diff-notes')).toContainText('hello.txt', { timeout: 10_000 })
      await shotDiffTab(page, 'diff-notepad.png')

      // Close the notepad so the file list regains full height: with the
      // tall notes drawer open, staged rows would fall below the list's
      // scroll fold and the final shot would miss them.
      await page.getByTestId('diff-notepad-toggle').click()

      // Stage the file: it moves from Unstaged to Staged. The staged diff
      // refetches after the move. Anchor every wait past the reload: the
      // staged hunk button exists only once staged hunks arrive, so the
      // row wait below can never match the stale unstaged table.
      await page.getByTestId('diff-stage-file').first().click()
      await expect(page.getByTestId('diff-unstage-file').first()).toBeVisible({ timeout: 30_000 })
      await expect(page.getByTestId('diff-unstage-hunk').first()).toBeVisible({ timeout: 30_000 })
      await expect(page.getByText('Loading diff…')).toHaveCount(0, { timeout: 30_000 })
      await expect(
        page.getByTestId('diff-file').locator('.diff-table-body').getByText('world').first(),
      ).toBeVisible({ timeout: 30_000 })
      await shotDiffTab(page, 'diff-staged.png')
      void id
    } finally {
      await deleteAllProjects(request)
    }
  })
})

test.describe('diff tab phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test.beforeEach(async ({ request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
  })

  test.afterEach(async ({ request }) => {
    await deleteAllProjects(request)
  })

  test('diff list renders on a phone viewport', async ({ page, request }) => {
    test.setTimeout(300_000)
    await openDiffTab(page, request, true)
    try {
      await expect(page.getByTestId('diff-file-list')).toBeVisible({ timeout: 30_000 })
      await expect(page.getByText('hello.txt')).toBeVisible()
      await shotDiffTab(page, 'diff-phone.png')
    } finally {
      await deleteAllProjects(request)
    }
  })
})
