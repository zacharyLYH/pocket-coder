import { expect, test } from '@playwright/test'
import { login } from './helpers'

// Auth flows against the real backend. The suite shares one logged-in
// session via storageState; this spec exercises the logged-out → logged-in
// transitions, so it opts out of the shared session entirely.

test.use({ storageState: { cookies: [], origins: [] } })

test('shows the login form when logged out', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send code' })).toBeVisible()
})

test('logs in with a PIN and shows the logged-in view', async ({ page }) => {
  await login(page)
})

test('logs out from the logged-in view', async ({ page }) => {
  await login(page)

  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
})
