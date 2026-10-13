import { execFile } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { ciTimeout, expect, test, type Route } from './test'
import { DATA_DIR } from './env'
import { createProject, deleteAllProjects, e2eRepo, engineUp, projectURL } from './helpers'

// Full-stack delete journey, real backend and real engine.
//
//   Phase 1 (optimism): a FAILING delete must still clear the row from view
//     the moment the user confirms — the row disappears, then the reconciling
//     refetch brings it back because the server still has it.
//   Phase 2 (the real thing): a succeeding delete must actually remove the
//     record from state.json AND tear down the project's container and both
//     volumes.
//
// The e2e backend runs in compose with the Docker socket mounted, so the
// project containers/volumes it creates land on the host engine this test
// inspects with the docker CLI.
//
// TIMEOUTS: a project container runs sleep as PID 1, so it ignores SIGTERM and
// docker stop waits out its full 10s grace period before SIGKILL — a delete
// therefore takes ~10s before state.json reflects it. Every poll below carries
// a budget longer than that rather than the default 5s.

const DELETE_BUDGET_MS = 30_000

// The forced-failure DELETE is held this long so the optimistic removal is
// observable; the "gone" assertion uses a shorter budget, so it can only pass
// if the row vanished before the server answered.
const FAIL_DELAY_MS = 800
const OPTIMISTIC_BUDGET_MS = 500

const DELETE_TIMEOUT = { timeout: ciTimeout(DELETE_BUDGET_MS) }

function dockerLines(args: string[]): Promise<string[]> {
  return new Promise((resolveOut) => {
    execFile('docker', args, { timeout: ciTimeout(30_000) }, (err, stdout) => {
      resolveOut(err ? [] : stdout.split('\n').map((s) => s.trim()).filter(Boolean))
    })
  })
}

const containers = () => dockerLines(['ps', '-a', '--format', '{{.Names}}'])
const volumes = () => dockerLines(['volume', 'ls', '--format', '{{.Name}}'])

// Mirror project.SanitizeName (server/internal/project/repo.go): ids are
// owner/repo, and Docker names cannot carry a slash.
function containerName(id: string): string {
  return `pcoder-${id.toLowerCase().replace(/[^a-z0-9_.-]+/g, '-').replace(/^[-.]+|[-.]+$/g, '')}`
}

test.describe('project delete (full stack)', () => {
  test('optimistically clears the row, then really deletes everything', async ({ page, request }) => {
    test.setTimeout(ciTimeout(240_000))
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    try {
      const id = await createProject(request, e2eRepo(1))
      const cname = containerName(id)
      const repoVol = `${cname}-repo`
      const homeVol = `${cname}-home`

      // Precondition: the created project really has a container + volumes,
      // so the assertions below prove deletion, not a no-op.
      await expect.poll(containers, DELETE_TIMEOUT).toContain(cname)
      await expect.poll(volumes, DELETE_TIMEOUT).toContain(repoVol)
      await expect.poll(volumes, DELETE_TIMEOUT).toContain(homeVol)

      await page.goto('/app')
      const card = page.getByTestId(`project-card-${id}`)
      await expect(card).toBeVisible({ timeout: ciTimeout(15_000) })

      const menu = page.getByTestId(`project-menu-${id}`)
      const isDelete = (url: URL): boolean =>
        url.pathname.startsWith('/api/projects/') && url.searchParams.get('scope') === 'all'
      const confirmDelete = async (): Promise<void> => {
        await menu.click()
        await page.getByRole('menuitem', { name: 'Delete project' }).click()
        await page.getByRole('button', { name: 'Delete', exact: true }).click()
      }

      // ── Phase 1: optimistic removal, forced failure ────────────────────
      // The DELETE fails, so the only way the row can leave the DOM is the
      // optimistic update; the refetch then restores it from server truth.
      const forceFail = async (route: Route): Promise<void> => {
        await new Promise((r) => setTimeout(r, FAIL_DELAY_MS))
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ error: 'e2e forced delete failure' }),
        })
      }
      await page.route(isDelete, forceFail)
      await confirmDelete()
      await expect(card).toHaveCount(0, { timeout: ciTimeout(OPTIMISTIC_BUDGET_MS) })
      await expect(card).toHaveCount(1, { timeout: ciTimeout(10_000) })
      await page.unroute(isDelete, forceFail)

      // ── Phase 2: the real delete ───────────────────────────────────────
      await confirmDelete()
      await expect(card).toHaveCount(0, { timeout: ciTimeout(10_000) })

      // Record gone: the project 404s and the list no longer carries it.
      await expect.poll(async () => (await request.get(`/api/projects/${projectURL(id)}`)).status(), DELETE_TIMEOUT).toBe(404)
      await expect
        .poll(async () => {
          const res = await request.get('/api/projects')
          return ((await res.json()) as { projects: { id: string }[] }).projects.map((p) => p.id)
        }, DELETE_TIMEOUT)
        .not.toContain(id)

      // state.json: the record is gone from the file too. Skipped only where
      // the file is unreadable — the compose server writes it root-owned, so
      // on a Linux CI bind mount the runner user gets EACCES. The 404 above
      // already proves the record left the store the file mirrors.
      let raw: string | null = null
      try {
        raw = readFileSync(resolve(DATA_DIR, 'state.json'), 'utf8')
      } catch {
        raw = null
      }
      if (raw !== null) expect(raw).not.toContain(id)

      // Container and both volumes torn down.
      await expect.poll(containers, DELETE_TIMEOUT).not.toContain(cname)
      await expect.poll(volumes, DELETE_TIMEOUT).not.toContain(repoVol)
      await expect.poll(volumes, DELETE_TIMEOUT).not.toContain(homeVol)
    } finally {
      await deleteAllProjects(request)
    }
  })
})


