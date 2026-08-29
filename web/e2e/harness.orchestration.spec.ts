import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp, resetHarnessRegistry } from './helpers'

async function waitForRunning(request: any, id: string) {
  for (let i = 0; i < 60; i++) {
    const res = await request.get(`/api/projects/${id}`)
    if (res.ok() && ((await res.json()) as { status: string }).status === 'running') return
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`project ${id} never reached running`)
}

async function fetchState(request: any) {
  const res = await request.get('/api/state')
  expect(res.ok()).toBeTruthy()
  return (await res.json()) as {
    projects: Record<string, { name: string; harnesses?: string[] }>
    harnesses: Record<string, { name: string }>
  }
}

async function gateShot(page: any, gate: string) {
  await expect(page).toHaveScreenshot(`harness-orchestration-${gate}-desktop.png`, { fullPage: true })
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page).toHaveScreenshot(`harness-orchestration-${gate}-mobile.png`, { fullPage: true })
  await page.setViewportSize({ width: 1280, height: 720 })
  await page.waitForTimeout(300)
}

async function createProjectViaUI(page: any, request: any, repoUrl: string, expectedName: string) {
  await page.getByPlaceholder(/Repo URL/).fill(repoUrl)
  await page.getByRole('button', { name: 'Create project' }).click()
  await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 30_000 })
  await page.reload()
  await expect(page.getByText(expectedName)).toBeVisible({ timeout: 10_000 })
  const orderRes = await request.get('/api/projects')
  const orderBody = (await orderRes.json()) as { projects: { id: string; name: string }[] }
  const created = orderBody.projects.find((p) => p.name === expectedName)
  if (!created) throw new Error(`project ${expectedName} not found after create`)
  await waitForRunning(request, created.id)
  return created.id
}

test.describe('harness installs are desired state', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('install 2 per project → picker → named launch → rename → restart (frontend, isolated)', async ({
    page,
    request,
  }) => {
    test.setTimeout(240_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)

    const helperInstall =
      `printf '#!/bin/sh\\nif [ $# -gt 0 ]; then echo "helper 1.0"; exit 0; fi\\nexec bash\\n' > /usr/local/bin/helper && chmod +x /usr/local/bin/helper`
    const addRes = await request.post('/api/harnesses', {
      data: { name: 'Helper', command: 'helper', install: helperInstall },
    })
    expect(addRes.status()).toBe(201)

    try {
      await page.goto('/')
      await expect(page.getByText('No projects yet.')).toBeVisible()
      const idAlpha = await createProjectViaUI(page, request, 'https://example.com/projAlpha', 'projAlpha')
      const idBeta = await createProjectViaUI(page, request, 'https://example.com/projBeta', 'projBeta')
      await page.getByPlaceholder(/Repo URL/).fill('')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 30_000 })
      await expect(page.getByText('untitled')).toBeVisible()
      const thirdRes = await request.get('/api/projects')
      const thirdBody = (await thirdRes.json()) as { projects: { id: string; name: string }[] }
      const third = thirdBody.projects.find((p) => p.name === 'untitled')
      if (!third) throw new Error('third project not found')
      await waitForRunning(request, third.id)

      await expect(page.getByText('projAlpha')).toBeVisible()
      await expect(page.getByText('projBeta')).toBeVisible()
      await expect(page.getByText('untitled')).toBeVisible()
      await gateShot(page, 'gate0-home-no-harnesses')

      const getOrder = async () => {
        const res = await request.get('/api/projects')
        const body = (await res.json()) as { projects: { id: string; name: string }[] }
        body.projects.sort((a, b) => (a.name !== b.name ? a.name.localeCompare(b.name) : a.id.localeCompare(b.id)))
        return body.projects.map((p) => p.id)
      }
      let order = await getOrder()
      const idxAlpha = order.indexOf(idAlpha)
      const idxBeta = order.indexOf(idBeta)
      const idxThird = order.indexOf(third.id)

      for (const harnessName of ['Crasher Demo', 'Helper']) {
        const row = page.locator('div.flex.items-center.justify-between', { hasText: harnessName })
        await expect(row).toBeVisible()
        await row.getByRole('button', { name: 'Install…' }).click()
        const picker = page.locator('div.mt-1.rounded-md').first()
        await expect(picker).toBeVisible()
        await picker.locator('label').nth(idxThird).locator('input').uncheck()
        await expect(picker.getByRole('button', { name: /Install in 2 project/ })).toBeVisible()
        await picker.getByRole('button', { name: /Install in 2 project/ }).click()
        await expect(page.getByText('Applied to 2 projects.')).toBeVisible({ timeout: 60_000 })
      }

      const rowCheck = page.locator('div.flex.items-center.justify-between', { hasText: 'Crasher Demo' })
      await rowCheck.getByRole('button', { name: 'Install…' }).click()
      const pickerCheck = page.locator('div.mt-1.rounded-md').first()
      await expect(pickerCheck).toBeVisible()
      await expect(pickerCheck.getByText('Installed')).toHaveCount(2)
      await expect(pickerCheck.locator('label').nth(idxAlpha).locator('input')).toBeDisabled()
      await expect(pickerCheck.locator('label').nth(idxBeta).locator('input')).toBeDisabled()
      await expect(pickerCheck.locator('label').nth(idxThird).locator('input')).toBeEnabled()
      await gateShot(page, 'gate1-installed')
      await pickerCheck.getByRole('button', { name: 'Cancel' }).click()

      const helperRow = page.locator('div.flex.items-center.justify-between', { hasText: 'Helper' })
      await helperRow.getByRole('button', { name: 'Install…' }).click()
      const helperPicker = page.locator('div.mt-1.rounded-md').first()
      await expect(helperPicker).toBeVisible()
      await expect(helperPicker.getByText('Installed')).toHaveCount(2)
      await expect(helperPicker.locator('label').nth(idxThird).locator('input')).toBeEnabled()
      await helperPicker.getByRole('button', { name: 'Cancel' }).click()

      const state1 = await fetchState(request)
      expect(state1.projects[idAlpha].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(state1.projects[idBeta].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(state1.projects[third.id].harnesses ?? []).toEqual([])
      expect(state1.harnesses['helper']).toBeTruthy()

      await page.getByRole('button', { name: 'Terminal' }).nth(idxAlpha).click()
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })
      await gateShot(page, 'gate2-terminal-open')

      await page.getByRole('button', { name: '+ New Session' }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog).toBeVisible()
      const nameInput = dialog.getByPlaceholder(/Session name/)
      await expect(nameInput).toBeVisible()
      await expect(nameInput).toHaveAttribute('required', '')

      const select = dialog.locator('select')
      const options = await select.locator('option').allTextContents()
      expect(options).toContain('Shell (bash)')
      expect(options.some((t) => t.includes('Crasher Demo'))).toBeTruthy()
      expect(options.some((t) => t.includes('Helper'))).toBeTruthy()
      expect(options.some((t) => t.includes('OpenCode'))).toBeFalsy()
      await select.selectOption('crasher-demo')
      await expect(dialog.getByRole('button', { name: 'Create & Attach' })).toBeDisabled()
      await expect(dialog).toHaveScreenshot('harness-dialog-requires-name-desktop.png')
      await page.setViewportSize({ width: 390, height: 844 })
      await page.waitForTimeout(300)
      await expect(dialog).toHaveScreenshot('harness-dialog-requires-name-mobile.png')
      await page.setViewportSize({ width: 1280, height: 720 })
      await dialog.getByRole('button', { name: 'Cancel' }).click()
      await expect(dialog).not.toBeVisible()

      await page.getByRole('button', { name: '+ New Session' }).click()
      const d1 = page.getByRole('dialog')
      await d1.getByPlaceholder(/Session name/).fill('shared-name')
      await d1.locator('select').selectOption('shell')
      await d1.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(d1).not.toBeVisible({ timeout: 15_000 })
      const sessionButton = page.getByRole('button', { name: 'Session', exact: true })
      await expect(sessionButton).toContainText('shared-name', { timeout: 10_000 })

      const bSessions1 = await request.get(`/api/projects/${idBeta}/sessions`)
      const bBody1 = (await bSessions1.json()) as { sessions: { name: string }[] }
      expect(bBody1.sessions.map((s) => s.name)).not.toContain('shared-name')
      await gateShot(page, 'gate3-shell-session')

      await page.getByRole('button', { name: '+ New Session' }).click()
      const d2 = page.getByRole('dialog')
      await d2.getByPlaceholder(/Session name/).fill('shared-name')
      await d2.locator('select').selectOption('crasher-demo')
      await d2.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(d2.getByText(/already exists|duplicate/i)).toBeVisible({ timeout: 10_000 })
      await expect(d2).toBeVisible()
      await d2.getByPlaceholder(/Session name/).fill('my-crasher')
      const createBtn = d2.getByRole('button', { name: 'Create & Attach' })
      await createBtn.click()
      await expect(d2.getByText('Launching my-crasher')).toBeVisible({ timeout: 5_000 })
      await expect(d2.getByText('Installing harness')).not.toBeVisible()
      await expect(d2).not.toBeVisible({ timeout: 15_000 })
      await expect(sessionButton).toContainText('my-crasher', { timeout: 10_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
      await gateShot(page, 'gate3-harness-session')

      await page.getByRole('button', { name: '+ New Session' }).click()
      const dHelper = page.getByRole('dialog')
      await dHelper.getByPlaceholder(/Session name/).fill('my-helper')
      await dHelper.locator('select').selectOption('helper')
      await dHelper.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(dHelper).not.toBeVisible({ timeout: 15_000 })
      await expect(sessionButton).toContainText('my-helper')
      const bSessions2 = await request.get(`/api/projects/${idBeta}/sessions`)
      const bBody2 = (await bSessions2.json()) as { sessions: { name: string }[] }
      expect(bBody2.sessions.map((s) => s.name)).not.toContain('my-helper')

      await expect(page.getByText('Connected')).toBeVisible({ timeout: 10_000 })
      await page.getByRole('button', { name: 'Rename' }).click()
      const renameDialog = page.getByRole('dialog', { name: /Rename session/ })
      await expect(renameDialog).toBeVisible()
      const renameInput = renameDialog.getByPlaceholder('New session name')
      await expect(renameInput).toHaveValue('my-helper')
      await renameInput.fill('renamed')
      await renameDialog.getByRole('button', { name: 'Rename' }).click()
      await expect(renameDialog).not.toBeVisible()
      await expect(sessionButton).toContainText('renamed')
      await expect(page).toHaveURL(/\/terminal\/renamed/)
      await sessionButton.click()
      await expect(page.getByRole('listbox')).toBeVisible()
      await expect(page.getByRole('option', { name: 'renamed' })).toBeVisible()
      await expect(page.getByRole('option', { name: 'my-crasher' })).toBeVisible()
      await expect(page.getByRole('option', { name: 'shared-name' })).toBeVisible()
      await gateShot(page, 'gate4-renamed')
      await page.getByRole('option', { name: 'renamed' }).click({ timeout: 10_000 })
      await expect(sessionButton).toContainText('renamed')
      const bSessions3 = await request.get(`/api/projects/${idBeta}/sessions`)
      const bBody3 = (await bSessions3.json()) as { sessions: { name: string }[] }
      expect(bBody3.sessions.map((s) => s.name)).not.toContain('renamed')
      await expect(page.getByText('Connected')).toBeVisible()
      await page.locator('.xterm-screen').click()
      await page.keyboard.type('echo after-rename\n')
      await expect
        .poll(async () => page.locator('.xterm-rows').innerText(), { timeout: 15_000 })
        .toContain('after-rename')
      const state2 = await fetchState(request)
      expect(state2.projects[idAlpha].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(state2.projects[idBeta].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(state2.projects[third.id].harnesses ?? []).toEqual([])

      await page.getByRole('button', { name: 'Restart' }).click()
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })
      await expect(sessionButton).toContainText('renamed')
      await sessionButton.click()
      await expect(page.getByRole('listbox')).toBeVisible()
      await expect(page.getByRole('option', { name: 'renamed' })).toBeVisible()
      await gateShot(page, 'gate5-restarted')
      await page.getByRole('option', { name: 'renamed' }).click({ timeout: 10_000 })
      await expect(sessionButton).toContainText('renamed')
      await page.waitForTimeout(1000)
      await page.locator('.xterm-screen').click()
      await page.keyboard.type('echo after-restart\n')
      await expect
        .poll(async () => page.locator('.xterm-rows').innerText(), { timeout: 30_000 })
        .toContain('after-restart')
      const bSessions4 = await request.get(`/api/projects/${idBeta}/sessions`)
      const bBody4 = (await bSessions4.json()) as { sessions: { name: string }[] }
      expect(bBody4.sessions.map((s) => s.name)).not.toContain('renamed')
      expect(bBody4.sessions.map((s) => s.name)).not.toContain('my-crasher')
      const thirdSessions = await request.get(`/api/projects/${third.id}/sessions`)
      const thirdSessBody = (await thirdSessions.json()) as { sessions: { name: string }[] }
      expect(thirdSessBody.sessions.map((s) => s.name)).not.toContain('renamed')

      await page.getByRole('button', { name: '+ New Session' }).click()
      const d3 = page.getByRole('dialog')
      await d3.getByPlaceholder(/Session name/).fill('renamed')
      await d3.locator('select').selectOption('crasher-demo')
      await d3.getByRole('button', { name: 'Create & Attach' }).click()
      await expect(d3.getByText(/already exists|duplicate/i)).toBeVisible()
      await d3.getByRole('button', { name: 'Cancel' }).click()

      const finalState = await fetchState(request)
      expect(finalState.projects[idAlpha].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(finalState.projects[idBeta].harnesses).toEqual(['crasher-demo', 'helper'])
      expect(finalState.projects[third.id].harnesses ?? []).toEqual([])
    } finally {
      await deleteAllProjects(request)
      await resetHarnessRegistry(request)
    }
  })
})
