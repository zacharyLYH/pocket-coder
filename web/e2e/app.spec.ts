import { expect, test } from '@playwright/test'
import { login } from './helpers'

// Auth flows against the real backend. The suite shares one logged-in
// session via storageState; this spec exercises the logged-out → logged-in
// transitions, so it opts out of the shared session entirely.

test.use({ storageState: { cookies: [], origins: [] } })

test('shows the landing page at / when logged out', async ({ page }) => {
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'Your dev box, in your pocket.' })).toBeVisible()
  await page.getByRole('button', { name: 'Log in' }).first().click()
  await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
})

test('shows the login form at /login when logged out', async ({ page }) => {
  await page.goto('/login')
  await expect(page.getByPlaceholder('you@example.com')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Send code' })).toBeVisible()
})

test('logs in with a PIN and lands in the app', async ({ page }) => {
  await login(page)
  await expect(page).toHaveURL(/\/app$/)
})

test('logs out back to the landing page', async ({ page }) => {
  await login(page)

  await page.getByRole('button', { name: 'Log out' }).click()
  await expect(page.getByRole('heading', { name: 'Your dev box, in your pocket.' })).toBeVisible()
})
