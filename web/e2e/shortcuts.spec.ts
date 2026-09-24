import { expect, test } from '@playwright/test'
import { createProject, deleteAllProjects, engineUp, projectURL } from './helpers'

test.describe('shortcuts', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('Home menu → shortcuts modal: add, save instantly, persist', async ({ page, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createProject(request)
      await page.goto('/')
      await expect(page.getByTestId(`project-card-${id}`)).toBeVisible({ timeout: 10_000 })
      const menu = page.getByTestId(`project-menu-${id}`)
      await expect(menu).toBeVisible()
      await menu.click()
      await page.getByRole('menuitem', { name: 'Shortcuts' }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await dialog.getByTestId('sc-add').click()
      await dialog.getByTestId('sc-alias-0').fill('dev')
      await dialog.getByTestId('sc-command-0').fill('npm run dev -- --host 0.0.0.0 --port 3000')
      await expect(dialog).toHaveScreenshot('shortcuts-modal.png')
      await dialog.getByTestId('sc-save').click()
      // Instant save: the row appears, the dialog stays open.
      await expect(dialog.getByText('dev', { exact: true })).toBeVisible()
      await dialog.press('Escape')
      await expect(dialog).not.toBeVisible({ timeout: 5_000 })
      const get = await request.get(`/api/projects/${projectURL(id)}`)
      const body = (await get.json()) as { shortcuts: { alias: string; command: string }[] }
      expect(body.shortcuts.find((s) => s.alias === 'dev')?.command).toBe('npm run dev -- --host 0.0.0.0 --port 3000')
      await expect(page).toHaveScreenshot('shortcuts-home-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('Terminal Shortcuts button → run shortcut', async ({ page, request }) => {
    test.setTimeout(60_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createProject(request)
      await request.patch(`/api/projects/${projectURL(id)}`, { data: { shortcuts: [{ id: 's-hello', alias: 'hello', kind: 'cmd', command: 'echo hello-quick' }] } })
      await page.goto('/')
      await expect(page.getByTestId(`project-card-${id}`)).toBeVisible({ timeout: 10_000 })
      await page.getByTestId(`project-card-${id}`).getByRole('button', { name: 'Terminal' }).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await page.getByTestId('tab-shortcuts').click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await expect(dialog.getByTestId('sc-run-hello')).toBeVisible()
      await expect(page).toHaveScreenshot('shortcuts-modal-run.png')
      await dialog.getByTestId('sc-run-hello').click()
      await expect(dialog).not.toBeVisible({ timeout: 5_000 })
      await expect(page.locator('.xterm-rows')).toContainText('hello-quick', { timeout: 10_000 })
      await expect(page).toHaveScreenshot('shortcuts-inject.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('shortcuts update and delete via modal', async ({ page, request }) => {
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const id = await createProject(request)
      await request.patch(`/api/projects/${projectURL(id)}`, { data: { shortcuts: [
        { id: 's-a', alias: 'a', kind: 'cmd', command: 'echo a' },
        { id: 's-b', alias: 'b', kind: 'cmd', command: 'echo b' },
      ] } })
      await page.goto('/')
      await page.getByTestId(`project-menu-${id}`).click()
      await page.getByRole('menuitem', { name: 'Shortcuts' }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      await expect(dialog).toHaveScreenshot('shortcuts-update-before.png')
      // update a, delete b (both save instantly)
      await dialog.getByRole('button', { name: 'Edit a' }).click()
      await dialog.getByTestId('sc-command-0').fill('echo a-updated')
      await dialog.getByTestId('sc-save').click()
      await expect(dialog.getByText('echo a-updated')).toBeVisible()
      await dialog.getByTestId('sc-delete-1').click()
      await expect(dialog.getByText('echo b')).not.toBeVisible({ timeout: 5_000 })
      await dialog.press('Escape')
      await expect(dialog).not.toBeVisible({ timeout: 5_000 })
      const body = (await (await request.get(`/api/projects/${projectURL(id)}`)).json()) as { shortcuts: { alias: string; command: string }[] }
      expect(body.shortcuts.find((s) => s.alias === 'a')?.command).toBe('echo a-updated')
      expect(body.shortcuts.find((s) => s.alias === 'b')).toBeUndefined()
      await expect(page).toHaveScreenshot('shortcuts-update-after.png', { fullPage: true })
    } finally {
      await deleteAllProjects(request)
    }
  })
})
