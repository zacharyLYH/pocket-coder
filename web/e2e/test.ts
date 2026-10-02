import { expect, test as base } from '@playwright/test'

// The ssh gate: Home refuses to render projects until POST /api/ssh/test
// answers, and that probe talks to GitHub for real — which always rejects
// the per-run key, gating every test behind the setup card. So each test
// starts with an optimistic stub. Registered before the test body, and
// page routes are matched last-in-first-out, so a spec that registers its
// own /api/ssh/test route (the gate-failure shot) still wins.
export const test = base.extend<{ sshProbe: void }>({
  sshProbe: [
    async ({ page }, use) => {
      await page.route('**/api/ssh/test', (route) =>
        route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, user: 'e2e' }) }))
      await use()
    },
    { auto: true },
  ],
})

export { expect }
// specs keep importing their helper types from the same one place
export type { APIRequestContext, Browser, BrowserContext, Locator, Page, Route } from '@playwright/test'
