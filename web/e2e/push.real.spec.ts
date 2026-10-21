import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { chromium, expect, test } from '@playwright/test'

import { AUTH_STATE, WEB_PORT, ciTimeout } from './env'

// Opt-in smoke against a real push service (FCM). It needs outbound
// network, can be rate limited, and is therefore kept out of the default
// gate: E2E_REAL_PUSH=1 npx playwright test e2e/push.real.spec.ts
//
// Persistent context is mandatory: Playwright's normal contexts are
// incognito and the Push API is unavailable there. Channel chromium is
// mandatory too — the headless shell reports notifications as denied.
test('real relay: subscribe, receive, unsubscribe, prune', async ({ request }) => {
  test.skip(process.env.E2E_REAL_PUSH !== '1', 'opt-in: needs outbound network')
  test.setTimeout(ciTimeout(240_000))

  const dir = await mkdtemp(join(tmpdir(), 'pcoder-push-real-'))
  const origin = `http://localhost:${WEB_PORT}`
  const context = await chromium.launchPersistentContext(dir, {
    channel: process.env.CI ? 'chromium' : 'chrome',
    headless: true,
    baseURL: origin,
  })
  try {
    const saved = JSON.parse(await readFile(AUTH_STATE, 'utf8'))
    await context.addCookies(saved.cookies)
    await context.grantPermissions(['notifications'], { origin })
    const page = await context.newPage()
    // The push card lives behind the ssh gate; stub the GitHub probe so the
    // real backend is reachable without GitHub credentials in CI.
    await page.route('**/api/ssh/test', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, user: 'e2e' }) }))
    await page.goto('/app')
    // Dev mode does not register the worker (PROD only), so install it here
    // before subscribing: the real PushManager needs a registration scope.
    await page.evaluate(async () => {
      await navigator.serviceWorker.register('/sw.js')
      await navigator.serviceWorker.ready
    })

    await page.getByTestId('push-enable').click({ timeout: ciTimeout(60_000) })
    await expect(page.getByTestId('push-test')).toBeVisible({ timeout: ciTimeout(60_000) })

    // enable() fires one test push; it must arrive through the real relay.
    await expect.poll(async () => page.evaluate(async () => {
      const reg = await navigator.serviceWorker.ready
      return (await reg.getNotifications()).map((n) => n.title)
    }), { timeout: ciTimeout(90_000) }).toContain('Pocket Coder')

    // Browser-side unsubscribe. FCM can keep accepting the endpoint for a
    // short grace period, so do not require an immediate 410 here; the Go
    // suite pins the prune on a real 404/410 response.
    await page.evaluate(async () => {
      const reg = await navigator.serviceWorker.ready
      const sub = await reg.pushManager.getSubscription()
      await sub?.unsubscribe()
    })
    await page.waitForTimeout(3000)
    const res = await request.post('/api/push', { data: { title: 't', body: 'b' } })
    expect(res.status()).toBe(200)
    const body = await res.json()
    expect(body.sent + body.failed + body.pruned).toBeGreaterThanOrEqual(1)
  } finally {
    await context.close()
    await rm(dir, { recursive: true, force: true })
  }
})
