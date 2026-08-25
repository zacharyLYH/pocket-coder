import { test as setup } from '@playwright/test'

import { AUTH_STATE } from './env'
import { login } from './helpers'

// Logs in once per run and saves the session cookie; every other project
// reuses it via storageState (playwright.config.ts).
setup('authenticate', async ({ page }) => {
  await login(page)
  await page.context().storageState({ path: AUTH_STATE })
})
