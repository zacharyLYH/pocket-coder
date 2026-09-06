import { readFileSync } from 'node:fs'
import { expect, test } from '@playwright/test'

import { DATA_DIR, SERVER_LOG } from './env'
import { deleteAllProjects, engineUp } from './helpers'

// Boot-bootstrap journey (the 422 regression): state.json is seeded BEFORE
// the backend boots (E2E_SEED=bootstrap.seed.json in e2e-parallel.sh) with a
// project that records opencode installed and a session oc1 — while Docker
// has NO container and NO volume for it. The server must rebuild the
// container, re-clone, and download opencode BEFORE it accepts requests.
// The user journey (navigating to the project terminal) then works with no
// lazy-install gap: the harness binary already exists when the session
// launches. From an empty state: no 422, no missing container, no missing
// harness. (The id e2eboost is deliberately not any dev project's id: e2e
// and dev share one engine, and container names are engine-global.)
test.describe('boot bootstrap from seeded state', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('seeded project terminal works straight after boot', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }

    try {
      // Boot proof, from the server's own log: bootstrap ran and finished
      // before the HTTP listener came up.
      const log = readFileSync(SERVER_LOG, 'utf8')
      expect(log).toContain('bootstrap: starting')
      expect(log).toContain('bootstrap: installing harness')
      expect(log).toContain('bootstrap: harness installed')
      expect(log.indexOf('bootstrap: harness installed')).toBeLessThan(log.indexOf('server listening'))

      // Seed proof: the e2eboost project survived boot and its container is
      // up (recreated by bootstrap, not lazily on first request).
      const projRes = await request.get('/api/projects/e2eboost')
      expect(projRes.status()).toBe(200)
      expect(((await projRes.json()) as { status: string }).status).toBe('running')

      // THE user journey that used to 422: navigate straight to the
      // recorded session's terminal. This is a plain page load — no API
      // pre-warming, no install call, nothing. Any lazy install left in the
      // launch pipeline would fail this with "not installed in this project".
      await page.goto('/projects/e2eboost/terminal/oc1')
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 30_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 30_000 })

      // The session runs `opencode || echo "[opencode exited: $?]"` — a
      // missing binary would paint the failure line instead of the TUI.
      await expect
        .poll(async () => page.locator('.xterm-rows').innerText(), { timeout: 120_000 })
        .toMatch(/Build|Connect|Ask anything/)

      // Belt and suspenders: the session-create API reports no validation
      // failure for this project.
      const eventsLog = readFileSync(`${DATA_DIR}/events.log`, 'utf8')
      expect(eventsLog).not.toContain('validation.failed')
    } finally {
      await deleteAllProjects(request)
    }
  })
})
