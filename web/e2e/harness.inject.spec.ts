import { expect, type APIRequestContext, type Page, test } from '@playwright/test'
import { FAKE_HARNESS_ID, FAKE_HARNESS_NAME, createProjectViaUI, deleteAllProjects, e2eRepo, e2eRepoID, engineUp, ensureFakeHarness, projectURL } from './helpers'

// The project-menu injection flow, end to end against the real backend:
//
//   1. seed two projects via the UI, inject a harness into ONE of them via
//      its project menu → Harnesses dialog (the picker defaults to that
//      project only)
//   2. verify only that project got the binary
//   3. a NEW project is NOT auto-injected — injecting into just it works
//   4. the home page's global Run card injects too (it runs the harness's
//      own install command as a plain command, proving they share
//      machinery)
//
// E2E Fake is the vehicle: its "download" is a local script — instant,
// while still exercising the real install+validate pipeline. Installed state
// is verified through the backend's own per-project probe.

async function projectOrder(request: APIRequestContext): Promise<string[]> {
  const res = await request.get('/api/projects')
  const body = (await res.json()) as { projects: { id: string }[] }
  return body.projects.map((p) => p.id)
}

async function fakeInstalled(request: APIRequestContext, id: string): Promise<boolean | undefined> {
  const res = await request.get(`/api/projects/${projectURL(id)}/harnesses`)
  expect(res.ok()).toBeTruthy()
  const body = (await res.json()) as { harnesses: { id: string; installed: boolean }[] }
  return body.harnesses.find((h) => h.id === FAKE_HARNESS_ID)?.installed
}

async function openHarnessDialog(page: Page, id: string) {
  await page.getByTestId(`project-menu-${id}`).click()
  await page.getByRole('menuitem', { name: /Harnesses/ }).click()
  const dialog = page.getByRole('dialog')
  await expect(dialog).toBeVisible()
  return dialog
}

test.describe('harness injection orchestration', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('injects only into selected projects; new projects opt in later', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await ensureFakeHarness(page.request)
    try {
      await page.goto('/')
      await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))
      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toHaveCount(1)
      await createProjectViaUI(page, page.request, e2eRepo(2), e2eRepoID(2))
      await expect(page.getByTestId(`project-card-${e2eRepoID(2)}`)).toHaveCount(1)
      const order1 = await projectOrder(page.request)
      const a = order1[0]
      const b = order1[1]

      // --- inject E2E Fake into ONLY the first project ---
      let dialog = await openHarnessDialog(page, a)
      const row = dialog.locator('div.flex.items-center.justify-between', { hasText: FAKE_HARNESS_NAME })
      await row.getByRole('button', { name: 'Install…' }).click()
      await dialog.getByRole('button', { name: /Install in 1 project/ }).click()
      await expect(dialog.getByText('Applied to 1 project.')).toBeVisible({ timeout: 60_000 })
      await page.keyboard.press('Escape')

      expect(await fakeInstalled(page.request, a)).toBe(true)
      expect(await fakeInstalled(page.request, b)).toBe(false)

      // --- a NEW project is not auto-injected ---
      await createProjectViaUI(page, page.request, e2eRepo(3), e2eRepoID(3))
      await expect(page.getByTestId(`project-card-${e2eRepoID(3)}`)).toHaveCount(1)
      const order2 = await projectOrder(page.request)
      const c = order2.find((id) => id !== a && id !== b)!
      expect(await fakeInstalled(page.request, c)).toBe(false)

      // ...but injecting into just it works
      dialog = await openHarnessDialog(page, c)
      const row2 = dialog.locator('div.flex.items-center.justify-between', { hasText: FAKE_HARNESS_NAME })
      await row2.getByRole('button', { name: 'Install…' }).click()
      await dialog.getByRole('button', { name: /Install in 1 project/ }).click()
      await expect(dialog.getByText('Applied to 1 project.')).toBeVisible({ timeout: 60_000 })

      expect(await fakeInstalled(page.request, c)).toBe(true)
      expect(await fakeInstalled(page.request, b)).toBe(false)

      // --- the home Run card injects too: run E2E Fake's own
      // install command as a plain command into the second project ---
      await page.keyboard.press('Escape')
      await page.goto('/')
      const harnesses = await (await page.request.get('/api/harnesses')).json()
      const fake = harnesses.harnesses.find((h: { id: string }) => h.id === FAKE_HARNESS_ID)
      await page.getByPlaceholder(/npm i -g opencode-ai@latest/).fill(fake.install)
      for (const id of await projectOrder(page.request)) {
        if (id !== b) await page.getByLabel(`Run in ${id}`).uncheck()
      }
      await page.getByRole('button', { name: 'Run in checked projects' }).click()
      await expect(page.getByText('Ran in 1 project.')).toBeVisible({ timeout: 60_000 })

      expect(await fakeInstalled(page.request, b)).toBe(true)
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
