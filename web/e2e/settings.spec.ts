import { expect, test } from './test'

import { deleteAllProjects, engineUp, createProject, e2eRepo, e2eRepoID, resetHarnessRegistry } from './helpers'

test.describe('settings card', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('settings card renders at the bottom of home with download and wipe buttons', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)
    await createProject(request, e2eRepo(1))
    try {
      await page.goto('/app')

      await expect(page.getByTestId('settings-card')).toBeVisible()
      await expect(page.getByTestId('settings-download')).toBeVisible()
      await expect(page.getByTestId('settings-wipe')).toBeVisible()
      await expect(page).toHaveScreenshot('settings-card.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('download button fetches state.json containing the project before wipe', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)
    await createProject(request, e2eRepo(1))
    try {
      await page.goto('/app')

      // Intercept the download fetch to verify it fires and returns valid state.
      let downloadHit = false
      let stateText = ''
      await page.route('**/api/state*', async (route) => {
        if (route.request().method() === 'GET') {
          downloadHit = true
          const res = await route.fetch()
          stateText = await res.text()
          await route.continue()
        } else {
          await route.continue()
        }
      })

      await page.getByTestId('settings-download').click()

      // Wait for the fetch to settle.
      await page.waitForFunction(() => true, { timeout: 3000 })

      expect(downloadHit).toBe(true)
      const state = JSON.parse(stateText)
      expect(state.user?.email).toBeTruthy()
      expect(state.serverKey?.privateKey).toBeTruthy()
      // The project we created is in state.json before the wipe.
      expect(Object.keys(state.projects ?? {})).toContain(e2eRepoID(1))
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('wipe flow downloads state then clears all projects from state.json', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)
    await createProject(request, e2eRepo(1))
    try {
      await page.goto('/app')
      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toBeVisible()

      // Intercept the download to verify it fires before the DELETE.
      let downloadHit = false
      await page.route('**/api/state*', async (route) => {
        if (route.request().method() === 'GET') {
          downloadHit = true
          await route.continue()
        } else {
          await route.continue()
        }
      })

      // Open confirmation dialog.
      await page.getByTestId('settings-wipe').click()
      await expect(page.getByText('Wipe all data?')).toBeVisible()
      await expect(page.getByTestId('settings-confirm-wipe')).toBeVisible()
      await expect(page.getByTestId('settings-cancel')).toBeVisible()
      await expect(page).toHaveScreenshot('settings-wipe-dialog.png', { fullPage: true })

      // Confirm wipe — this triggers download then DELETE.
      await page.getByTestId('settings-confirm-wipe').click()

      // Wait for the wipe to finish (projects list is empty).
      await expect(async () => {
        const res = await request.get('/api/projects')
        expect(res.ok()).toBeTruthy()
        const body = await res.json()
        expect(body.projects).toHaveLength(0)
      }).toPass({ timeout: 30_000 })

      // Download was triggered before the wipe.
      expect(downloadHit).toBe(true)

      // Project card is gone from the page.
      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toHaveCount(0)

      // Settings card is still visible (page didn't crash).
      await expect(page.getByTestId('settings-card')).toBeVisible()

      // Verify state.json was actually cleared: projects empty,
      // user + server key preserved.
      const stateRes = await request.get('/api/state')
      expect(stateRes.ok()).toBeTruthy()
      const state = await stateRes.json()
      expect(Object.keys(state.projects ?? {})).toHaveLength(0)
      expect(state.user?.email).toBeTruthy()
      expect(state.serverKey?.privateKey).toBeTruthy()
    } finally {
      await deleteAllProjects(request)
      await resetHarnessRegistry(request)
    }
  })

  test('cancel button on wipe dialog aborts without deleting', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)
    await createProject(request, e2eRepo(1))
    try {
      await page.goto('/app')
      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toBeVisible()

      await page.getByTestId('settings-wipe').click()
      await expect(page.getByText('Wipe all data?')).toBeVisible()

      // Cancel — no DELETE /api/state should fire.
      const deleteStarted = page.waitForResponse((res) => res.url().includes('/api/state') && res.request().method() === 'DELETE', { timeout: 1000 }).catch(() => null)
      await page.getByTestId('settings-cancel').click()
      await expect(page.getByText('Wipe all data?')).toHaveCount(0)

      const deleteRes = await deleteStarted
      expect(deleteRes).toBeNull()

      // Project still exists.
      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toBeVisible()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
