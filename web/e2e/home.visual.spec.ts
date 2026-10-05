import { expect, test } from './test'
import { FAKE_HARNESS_NAME, createProjectViaUI, deleteAllProjects, e2eRepo, e2eRepoID, ensureFakeHarness, resetHarnessRegistry, engineUp } from './helpers'

// Visual + behavioral tests for the home screen against the real backend:
// login form, the ssh gate in front of the projects, the clone form, and
// the server-key card.
//
// The logged-in shots need a clean, known backend state, so each test wipes
// projects first and cleans up whatever it created — order-proof within
// a run (the data dir itself is reset per run by playwright.config.ts).

// The login form tests need the logged-out screen: opt out of the shared
// session with an empty storageState.
const loggedOut = { storageState: { cookies: [], origins: [] } }

test.describe('login form', () => {
  test.use({ viewport: { width: 1280, height: 720 }, storageState: loggedOut.storageState })

  test('login form renders on desktop', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
    await expect(page).toHaveScreenshot('login-desktop.png', { fullPage: true })
  })
})

test.describe('login form phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, storageState: loggedOut.storageState })

  test('login form renders on phone', async ({ page }) => {
    await page.goto('/login')
    await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
    await expect(page).toHaveScreenshot('login-phone.png', { fullPage: true })
  })
})

test.describe('home screen', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('a failing ssh probe gates the projects behind the setup card', async ({ page }) => {
    // POST /api/ssh/test is the one call Home makes before showing
    // projects. Fail it and the page hands over the key card plus the
    // GitHub keys link instead of the project list.
    await page.route('**/api/ssh/test', (route) =>
      route.fulfill({ status: 502, contentType: 'application/json', body: JSON.stringify({ error: 'GitHub rejected the key' }) }))
    await page.goto('/app')

    await expect(page.getByTestId('ssh-gate')).toBeVisible()
    await expect(page.getByTestId('github-keys-link')).toHaveAttribute('href', 'https://github.com/settings/keys')
    await expect(page.getByTestId('git-public-key')).toContainText('ssh-')
    // nothing project-shaped renders behind the gate
    await expect(page.getByText('No projects yet.')).toHaveCount(0)
    await expect(page.getByText('Run a command')).toHaveCount(0)
    // the key text and fingerprint are generated per run — mask them
    await expect(page).toHaveScreenshot('home-ssh-gate.png', {
      fullPage: true,
      mask: [page.getByTestId('git-public-key'), page.getByTestId('git-fingerprint')],
    })
  })

  test('home screen with a project renders', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    try {
      await page.goto('/app')
      await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))
      await page.reload()

      await expect(page.getByTestId(`project-card-${e2eRepoID(1)}`)).toBeVisible()
      // one Git row for the server deploy key; no per-user key registry
      await expect(page.getByTestId('setup-git')).toBeVisible()
      await expect(page.getByTestId('setup-ssh')).toHaveCount(0)
      await expect(page).toHaveScreenshot('home-with-project.png', { fullPage: true })
    } finally {
      await deleteAllProjects(page.request)
    }
  })

  test('home screen with no projects', async ({ page }) => {
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    await page.goto('/app')

    await expect(page.getByText('No projects yet.')).toBeVisible()
    // two connection rows: Git (server key fingerprint) and AI
    await expect(page.getByTestId('setup-git')).toBeVisible()
    await expect(page.getByTestId('setup-ai')).toBeVisible()
    await expect(page.getByTestId('setup-ssh')).toHaveCount(0)
    await expect(page).toHaveScreenshot('home-empty.png', { fullPage: true })
  })

  test('clone form takes either URL shape with no method picker', async ({ page }) => {
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    await page.goto('/app')

    await page.getByPlaceholder(/clone URL/i).fill('https://github.com/x/hello.git')
    // no HTTPS/SSH radios: one field, the server normalizes every paste
    await expect(page.getByRole('radio')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Clone project' })).toBeEnabled()
    await expect(page).toHaveScreenshot('home-clone-form.png', { fullPage: true })
  })

  test('Git dialog shows the server key with test and regenerate', async ({ page }) => {
    await page.goto('/app')
    await page.getByTestId('setup-git').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByTestId('git-public-key')).toContainText('ssh-')
    await expect(dialog.getByTestId('git-test')).toBeEnabled()
    await expect(dialog.getByTestId('git-regen')).toBeEnabled()
    // the keypair is generated per run — mask it and the fingerprint
    await expect(dialog).toHaveScreenshot('git-dialog.png', {
      mask: [dialog.getByTestId('git-public-key'), dialog.getByTestId('git-fingerprint')],
    })
  })

  test('harness suggestions render and install explicitly', async ({ page }) => {
    test.skip(!(await engineUp(page.request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    await ensureFakeHarness(page.request)
    try {
      await page.goto('/app')
      const id = await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))

      // install from the project menu → Harnesses dialog
      await page.getByTestId(`project-menu-${id}`).click()
      await page.getByRole('menuitem', { name: /Harnesses/ }).click()
      const dialog = page.getByRole('dialog')
      // suggestions include the seeded agent CLIs, OpenCode among them
      await expect(dialog.getByText('OpenCode', { exact: true })).toBeVisible()

      // an explicit install runs synchronously and surfaces the outcome here.
      // E2E Fake is used because its "download" is a local script — fast,
      // while still exercising the real install+validate endpoint. The dialog
      // is scoped to this project, so Install is one click — no picker.
      const row = dialog.locator('div.flex.items-center.justify-between', { hasText: FAKE_HARNESS_NAME })
      await expect(row.getByRole('button', { name: 'Install', exact: true })).toBeVisible()
      await expect(page).toHaveScreenshot('harness-install-dialog.png', { fullPage: true })
      await row.getByRole('button', { name: 'Install', exact: true }).click()
      await expect(dialog.getByText('Installed.')).toBeVisible({ timeout: 120_000 })
    } finally {
      await deleteAllProjects(page.request)
    }
  })

  test('busy overlay appears during harness install', async ({ page, request }) => {
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(page.request)
    await resetHarnessRegistry(page.request)
    await ensureFakeHarness(page.request)
    try {
      await page.route('/api/harnesses/e2e-fake/install', async (route) => {
        await new Promise((r) => setTimeout(r, 2000))
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ results: [{ project: 'p1', status: 'ok' }] }) })
      })
      await page.goto('/app')
      const id = await createProjectViaUI(page, page.request, e2eRepo(1), e2eRepoID(1))
      await page.getByTestId(`project-menu-${id}`).click()
      await page.getByRole('menuitem', { name: /Harnesses/ }).click()
      const dialog = page.getByRole('dialog')
      await expect(dialog.getByText(FAKE_HARNESS_NAME)).toBeVisible()

      const row = dialog.locator('div.flex.items-center.justify-between', { hasText: FAKE_HARNESS_NAME })
      // one click installs into this project; the mocked 2s round-trip keeps
      // the busy label up long enough to capture it
      await row.getByRole('button', { name: 'Install', exact: true }).click()
      await expect(dialog.getByRole('button', { name: 'Installing…' })).toBeVisible({ timeout: 10_000 })
      await expect(page).toHaveScreenshot('home-busy-installing.png')
    } finally {
      await deleteAllProjects(page.request)
    }
  })
})
