import { expect, test } from '@playwright/test'
import { createBlankProject, deleteAllProjects, engineUp } from './helpers'

test.describe('quick commands', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('Home dropdown → quick commands modal: add, save, persist', async ({ page, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createBlankProject(request)
      await page.goto('/')
      await expect(page.getByText('untitled')).toBeVisible({ timeout: 10_000 })
      const menu = page.getByTestId(`project-menu-${id}`)
      await expect(menu).toBeVisible()
      await menu.click()
      await page.getByRole('menuitem', { name: 'Update quick commands' }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await dialog.getByTestId('qc-add').click()
      await dialog.getByTestId('qc-alias-0').fill('dev')
      await dialog.getByTestId('qc-command-0').fill('npm run dev -- --host 0.0.0.0 --port 3000')
      await expect(dialog).toHaveScreenshot('quickcommands-modal.png')
      await dialog.getByTestId('qc-save').click()
      await expect(dialog).not.toBeVisible({ timeout: 5_000 })
      const get = await request.get(`/api/projects/${id}`)
      const body = (await get.json()) as { quickCommands: Record<string, string> }
      expect(body.quickCommands.dev).toBe('npm run dev -- --host 0.0.0.0 --port 3000')
      await page.getByTestId(`project-menu-${id}`).click()
      await expect(page.getByRole('menuitem', { name: 'Update quick commands' })).toBeVisible()
      await expect(page).toHaveScreenshot('quickcommands-home-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('Terminal Commands dropdown → inject quick command', async ({ page, request }) => {
    test.setTimeout(60_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createBlankProject(request)
      await request.patch(`/api/projects/${id}`, { data: { quickCommands: { hello: 'echo hello-quick' } } })
      await page.goto('/')
      await expect(page.getByText('untitled')).toBeVisible({ timeout: 10_000 })
      await page.getByTestId(`project-card-${id}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await page.getByTestId('terminal-actions-trigger').click()
      await expect(page.getByTestId('qc-run-hello')).toBeVisible()
      await expect(page).toHaveScreenshot('quickcommands-dropdown.png')
      await page.getByTestId('qc-run-hello').click()
      await page.waitForTimeout(1000)
      await expect(page.locator('.xterm-rows')).toContainText('hello-quick', { timeout: 10_000 })
      await expect(page).toHaveScreenshot('quickcommands-inject.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('quick commands update and delete via modal', async ({ page, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createBlankProject(request)
      await request.patch(`/api/projects/${id}`, { data: { quickCommands: { a: 'echo a', b: 'echo b' } } })
      await page.goto('/')
      await page.getByTestId(`project-menu-${id}`).click()
      await page.getByRole('menuitem', { name: 'Update quick commands' }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await expect(dialog).toHaveScreenshot('quickcommands-update-before.png')
      // update a, delete b
      await dialog.getByTestId('qc-command-0').fill('echo a-updated')
      await dialog.getByTestId('qc-delete-1').click()
      await dialog.getByTestId('qc-save').click()
      await expect(dialog).not.toBeVisible({ timeout: 5_000 })
      let body = (await (await request.get(`/api/projects/${id}`)).json()) as { quickCommands: Record<string, string> }
      expect(body.quickCommands.a).toBe('echo a-updated')
      expect(body.quickCommands.b).toBeUndefined()
      // via Terminal modal delete remaining
      await page.getByTestId(`project-card-${id}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('qc-manage').click()
      const d2 = page.getByRole('dialog')
      await expect(d2).toBeVisible()
      await d2.getByTestId('qc-delete-0').click()
      await d2.getByTestId('qc-save').click()
      await expect(d2).not.toBeVisible({ timeout: 5_000 })
      body = (await (await request.get(`/api/projects/${id}`)).json()) as { quickCommands: Record<string, string> }
      expect(body.quickCommands == null || Object.keys(body.quickCommands ?? {}).length === 0).toBeTruthy()
      await expect(page).toHaveScreenshot('quickcommands-update-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })
})
