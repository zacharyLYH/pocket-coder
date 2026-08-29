import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'

// The home-page injection flow, end to end against the real backend:
//
//   1. seed two projects via the UI, inject a harness into ONE of them via the picker
//   2. verify only that project got the binary
//   3. a NEW project is NOT auto-injected — but shows up in the picker, and
//      injecting into just it works
//   4. the arbitrary command box injects too (it runs the harness's own
//      install command as a plain command, proving they share machinery)
//
// Crasher Demo is the vehicle: its "download" is a local script — instant,
// while still exercising the real install+validate pipeline. Installed state
// is verified through the backend's own per-project probe.

async function createProjectViaUI(page: any) {
  await page.getByPlaceholder(/Repo URL/).fill('')
  await page.getByRole('button', { name: 'Create project' }).click()
  await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 30_000 })
}

async function projectOrder(request: any): Promise<string[]> {
  const res = await request.get('/api/projects')
  const body = (await res.json()) as { projects: { id: string }[] }
  return body.projects.map((p) => p.id)
}

async function crasherInstalled(request: any, id: string): Promise<boolean | undefined> {
  const res = await request.get(`/api/projects/${id}/harnesses`)
  expect(res.ok()).toBeTruthy()
  const body = (await res.json()) as { harnesses: { id: string; installed: boolean }[] }
  return body.harnesses.find((h) => h.id === 'crasher-demo')?.installed
}

test.describe('harness injection orchestration', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('injects only into selected projects; new projects opt in later', async ({ page }) => {
    test.setTimeout(180_000)
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    try {
      await page.goto('/')
      await expect(page.getByText('No projects yet.')).toBeVisible()
      await createProjectViaUI(page)
      await expect(page.getByText('untitled')).toHaveCount(1)
      await createProjectViaUI(page)
      await expect(page.getByText('untitled')).toHaveCount(2)
      const order1 = await projectOrder(page.request)
      const a = order1[0]
      const b = order1[1]

      // --- inject Crasher Demo into ONLY the first project ---
      const row = page.locator('div.flex.items-center.justify-between', { hasText: 'Crasher Demo' })
      await row.getByRole('button', { name: 'Install…' }).click()
      const picker = page.locator('div.mt-1.flex.flex-col', { has: page.getByRole('button', { name: /Install in/ }) })
      await picker.locator('label').nth((await projectOrder(page.request)).indexOf(b)).locator('input').uncheck()
      await picker.getByRole('button', { name: /Install in 1 project/ }).click()
      await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 60_000 })

      expect(await crasherInstalled(page.request, a)).toBe(true)
      expect(await crasherInstalled(page.request, b)).toBe(false)

      // --- a NEW project is not auto-injected ---
      await createProjectViaUI(page)
      await expect(page.getByText('untitled')).toHaveCount(3)
      const order2 = await projectOrder(page.request)
      const c = order2.find((id) => id !== a && id !== b)!
      expect(await crasherInstalled(page.request, c)).toBe(false)

      // ...but it appears in the picker once the page knows about it, and
      // injecting into just it works
      await row.getByRole('button', { name: 'Install…' }).click()
      const picker2 = page.locator('div.mt-1.flex.flex-col', { has: page.getByRole('button', { name: /Install in/ }) })
      await expect(picker2.locator('label')).toHaveCount(3)
      for (const [i, id] of (await projectOrder(page.request)).entries()) {
        if (id !== c) {
          const input = picker2.locator('label').nth(i).locator('input')
          if (await input.isEnabled()) await input.uncheck()
        }
      }
      await picker2.getByRole('button', { name: /Install in 1 project/ }).click()
      await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 60_000 })

      expect(await crasherInstalled(page.request, c)).toBe(true)
      expect(await crasherInstalled(page.request, b)).toBe(false)

      // --- the arbitrary command box injects too: run Crasher Demo's own
      // install command as a plain command into the second project ---
      const harnesses = await (await page.request.get('/api/harnesses')).json()
      const crasher = harnesses.harnesses.find((h: { id: string }) => h.id === 'crasher-demo')
      await page.getByPlaceholder(/npm i -g opencode-ai@latest/).fill(crasher.install)
      await page.getByRole('button', { name: 'Choose projects…' }).click()
      const cmdPicker = page.locator('form', { has: page.getByRole('button', { name: /Run in/ }) })
      for (const [i, id] of (await projectOrder(page.request)).entries()) {
        if (id !== b) await cmdPicker.locator('label').nth(i).locator('input').uncheck()
      }
      await cmdPicker.getByRole('button', { name: /Run in 1 project/ }).click()
      await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 60_000 })

      expect(await crasherInstalled(page.request, b)).toBe(true)
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
