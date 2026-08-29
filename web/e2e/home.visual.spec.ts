import { expect, test } from '@playwright/test'
import { deleteAllProjects, deleteAllSSHKeys, resetHarnessRegistry, engineUp } from './helpers'

// Visual + behavioral tests for the home screen against the real backend:
// login form, project list, SSH keys card, clone method toggle.
//
// The logged-in shots need a clean, known backend state, so each test wipes
// projects/keys first and cleans up whatever it created — order-proof within
// a run (the data dir itself is reset per run by playwright.config.ts).

// The login form tests need the logged-out screen: opt out of the shared
// session with an empty storageState.
const loggedOut = { storageState: { cookies: [], origins: [] } }

test.describe('login form', () => {
  test.use({ viewport: { width: 1280, height: 720 }, storageState: loggedOut.storageState })

  test('login form renders on desktop', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
    await expect(page).toHaveScreenshot('login-desktop.png', { fullPage: true })
  })
})

test.describe('login form phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, storageState: loggedOut.storageState })

  test('login form renders on phone', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
    await expect(page).toHaveScreenshot('login-phone.png', { fullPage: true })
  })
})

test.describe('home screen', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('home screen with a project and SSH keys renders', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await deleteAllSSHKeys(page.request)
    await resetHarnessRegistry(page.request)
    try {
      // one real blank-project ("untitled" — the name is derived
      // from the repo URL, and there is none) and two real keys
      await page.goto('/')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 300_000 })
      await page.request.post('/api/ssh-keys', {
        data: { publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIWorkLaptopKey', label: 'work-laptop' },
      })
      await page.request.post('/api/ssh-keys', {
        data: { publicKey: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHomeMachineKey', label: 'home' },
      })
      await page.reload()

      await expect(page.getByText('untitled')).toBeVisible()
      await expect(page.getByText('work-laptop')).toBeVisible()
      await expect(page.getByText('home')).toBeVisible()
      await expect(page).toHaveScreenshot('home-with-projects-and-keys.png', { fullPage: true })
    } finally {
      await deleteAllProjects(page.request)
      await deleteAllSSHKeys(page.request)
    }
  })

  test('home screen with no projects and no SSH keys', async ({ page }) => {
    await deleteAllProjects(page.request)
    await deleteAllSSHKeys(page.request)
    await resetHarnessRegistry(page.request)
    await page.goto('/')

    await expect(page.getByText('No projects yet.')).toBeVisible()
    await expect(page.getByText('No keys registered.')).toBeVisible()
    await expect(page).toHaveScreenshot('home-empty.png', { fullPage: true })
  })

  test('clone method toggle appears when repo URL is entered', async ({ page }) => {
    await resetHarnessRegistry(page.request)
    await page.goto('/')

    // no toggle visible yet
    await expect(page.getByText('Clone via:')).not.toBeVisible()

    // type a repo URL
    await page.getByPlaceholder(/Repo URL/).fill('https://github.com/x/hello.git')
    await expect(page.getByText('Clone via:')).toBeVisible()
    await expect(page.getByRole('button', { name: 'HTTPS' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'SSH' })).toBeVisible()
    await expect(page).toHaveScreenshot('home-clone-toggle-visible.png', { fullPage: true })
  })

  test('SSH toggle shows key status', async ({ page }) => {
    await deleteAllSSHKeys(page.request)
    await resetHarnessRegistry(page.request)
    await page.goto('/')

    await page.getByPlaceholder(/Repo URL/).fill('git@github.com:x/hello.git')
    await page.getByRole('button', { name: 'SSH' }).click()
    await expect(page.getByText('No SSH keys')).toBeVisible()
    await expect(page).toHaveScreenshot('home-ssh-no-keys.png')
  })

  test('harness suggestions render and install explicitly', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    try {
      await page.goto('/')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 300_000 })

      // suggestions include the seeded agent CLIs, OpenCode among them
      await expect(page.getByText('OpenCode', { exact: true })).toBeVisible()

      // an explicit install runs synchronously and surfaces the outcome here.
      // Crasher Demo is used because its "download" is a local script — fast,
      // while still exercising the real install+validate endpoint. The picker
      // defaults to every project checked; with one project that is a 1:1 run.
      const row = page.locator('div.flex.items-center.justify-between', { hasText: 'Crasher Demo' })
      await row.getByRole('button', { name: 'Install…' }).click()
      // the project picker is part of the feature's surface — capture it open
      await expect(page.getByRole('button', { name: /Install in 1 project/ })).toBeVisible()
      await expect(page).toHaveScreenshot('harness-project-picker.png', { fullPage: true })
      await page.getByRole('button', { name: /Install in 1 project/ }).click()
      await expect(page.getByText('Applied to 1 project.')).toBeVisible({ timeout: 120_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })

  test('busy overlay appears during harness install', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    try {
      await page.route('/api/harnesses/crasher-demo/install', async (route) => {
        await new Promise((r) => setTimeout(r, 2000))
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ results: [{ project: 'p1', status: 'ok' }] }) })
      })
      await page.goto('/')
      await page.getByRole('button', { name: 'Create project' }).click()
      await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 300_000 })
      await expect(page.getByText('Crasher Demo')).toBeVisible()

      const row = page.locator('div.flex.items-center.justify-between', { hasText: 'Crasher Demo' })
      await row.getByRole('button', { name: 'Install…' }).click()
      await page.getByRole('button', { name: /Install in 1 project/ }).click()

      await expect(page.locator('p.text-muted-foreground').filter({ hasText: 'Working…' })).toBeVisible({ timeout: 5_000 })
      await expect(page).toHaveScreenshot('home-busy-installing.png')
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})

