import { expect, type APIRequestContext, test } from '@playwright/test'
import { createProject, deleteAllProjects, engineUp, projectURL, resetHarnessRegistry, terminalUrl, waitForRunning } from './helpers'

// Read a marker file's mtime from inside the container. Returns 0 if absent.
async function markerMtime(request: APIRequestContext, id: string): Promise<number> {
  const res = await request.post('/api/projects/exec', {
    data: { projectIds: [id], command: 'stat -c %Y /tmp/harness-started 2>/dev/null || echo 0' },
  })
  const body = (await res.json()) as { results: { status: string; detail: string }[] }
  return parseInt(body.results[0]?.detail?.trim() ?? '0', 10) || 0
}

test.describe('harness relaunch', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('restart and re-entry relaunch dead harness sessions', async ({ page, request }) => {
    test.setTimeout(300_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    await resetHarnessRegistry(request)

    // Helper harness: writes a timestamp marker on startup, then execs bash.
    const installCmd =
      "printf '#!/bin/sh\\nif [ $# -gt 0 ]; then echo \"helper 1.0\"; exit 0; fi\\ndate +%%s > /tmp/harness-started\\nexec bash\\n' > /usr/local/bin/helper && chmod +x /usr/local/bin/helper"
    const addRes = await request.post('/api/harnesses', {
      data: { name: 'Helper', command: 'helper', install: installCmd },
    })
    expect(addRes.status()).toBe(201)

    try {
      // Create project via API (fast)
      const id = await createProject(request)
      await waitForRunning(request, id)

      // Install harness in project via API
      const installRes = await request.post(`/api/harnesses/helper/install`, {
        data: { projectIds: [id] },
      })
      expect(installRes.ok()).toBeTruthy()

      // Launch harness session via API (auto-named helper-1)
      const launchRes = await request.post(`/api/projects/${projectURL(id)}/sessions`, {
        data: { harnessId: 'helper' },
      })
      expect(launchRes.status()).toBe(201)
      const sessionName = (await launchRes.json()) as { name: string }

      // Navigate to terminal and attach via UI
      await page.goto(terminalUrl(id, sessionName.name))
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 15_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })

      // Wait for harness to start and write its marker
      await page.waitForTimeout(2000)
      const t0 = await markerMtime(request, id)
      expect(t0).toBeGreaterThan(0)

      // --- Part 1: restart relaunches the harness ---
      // Kill the harness process: get the pane PID from tmux and kill just
      // that process (not the tmux server, which also has 'helper' in its tree).
      await request.post('/api/projects/exec', {
        data: { projectIds: [id], command: `tmux list-panes -t ${sessionName.name} -F '#{pane_pid}' | head -1 | xargs kill 2>/dev/null || true` },
      })
      await page.waitForTimeout(2000)

      // Restart
      await page.getByTestId('terminal-actions-trigger').click()
      await page.getByTestId('terminal-action-restart').click()
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 15_000 })

      // Wait for harness to restart and write a new marker
      await page.waitForTimeout(2000)
      const t1 = await markerMtime(request, id)
      expect(t1).toBeGreaterThan(t0) // marker was rewritten → harness command ran

      // --- Part 2: re-entry cleans dead sessions ---
      // Kill the harness session via the API
      await request.delete(`/api/projects/${projectURL(id)}/sessions/${sessionName.name}`)
      await page.waitForTimeout(2000)

      // Go home, then re-enter the terminal (navigate away and back)
      await page.goto('/')
      await page.goto(terminalUrl(id, sessionName.name))
      // The ensure path runs LaunchNamed (validateCLI + tmux create),
      // which can take ~20s. Be generous.
      await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: 30_000 })
      await expect(page.getByText('Connected')).toBeVisible({ timeout: 20_000 })

      // Wait for dead-session cleanup + harness relaunch
      await page.waitForTimeout(3000)
      const t2 = await markerMtime(request, id)
      expect(t2).toBeGreaterThan(t1) // marker rewritten again → harness relaunched on re-entry
    } finally {
      await deleteAllProjects(request)
      await resetHarnessRegistry(request)
    }
  })
})
