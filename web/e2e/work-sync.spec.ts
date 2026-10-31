import { execFileSync, spawn, type ChildProcessWithoutNullStreams } from 'node:child_process'
import { mkdirSync, readdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join, resolve } from 'node:path'

import { expect, test, type APIRequestContext, type Disposable, type Page } from '@playwright/test'

import { API_PORT, ciTimeout, GIT_PORT, RUN_DIR, RUN_SLUG } from './env'
import { createProject, deleteAllProjects, e2eRepo, e2eRepoID, projectURL, terminalUrl, waitForRunning } from './helpers'

// Work Sync end to end: pcoder's dialog, the real static client, and the
// lock that makes the project read-only while bytes move.
//
// The journey is deliberately sequential — download to the laptop, change
// the laptop copy, upload back, verify BOTH sides, then download again into
// a second clone. Screenshots are attached as test artifacts (modal, both
// lock states, before/after diff, error state).
const BACKEND = `http://127.0.0.1:${API_PORT}`
const WORK = join(RUN_DIR, 'work-sync')
const HOME = join(WORK, 'home')
const BIN = resolve(WORK, 'pcoder-sync')
const SLOT = 4

test.describe.configure({ mode: 'serial' })
test.use({ viewport: { width: 1280, height: 720 } })

test.describe('work sync', () => {
  let projectId: string
  let localA: string
  let localB: string

  test.beforeAll(async ({ request }) => {
    rmSync(WORK, { recursive: true, force: true })
    mkdirSync(HOME, { recursive: true })
    // The interactive lock screenshots need the client's stdin, which a
    // `curl | sh` pipeline owns. Build the same client the bootstrap would
    // download and drive it directly; one bootstrap run below still covers
    // the launcher path.
    execFileSync('go', ['build', '-o', BIN, './cmd/pcoder-sync'], {
      cwd: resolve(process.cwd(), '../server'),
      stdio: 'ignore',
      timeout: ciTimeout(180_000),
    })
    projectId = await createProject(request, e2eRepo(SLOT))
    await waitForRunning(request, projectId)
    expect(projectId).toBe(e2eRepoID(SLOT))

    localA = join(WORK, 'local-a')
    localB = join(WORK, 'local-b')
    clone(localA)
    clone(localB)
  })

  test.afterAll(async ({ request }) => {
    await deleteAllProjects(request)
    rmSync(WORK, { recursive: true, force: true })
  })

  test('download, change, upload, verify both sides', async ({ page, request }, testInfo) => {
    test.setTimeout(ciTimeout(420_000))
    const shot = async (name: string, baseline = false) => {
      // Baseline shots (the lock and dialog states this feature is about) are
      // asserted against a checked-in PNG. The code, its countdown, and the
      // project id are volatile/run-scoped, so they are masked to keep the
      // baseline deterministic. Every shot is also written next to the test
      // output AND attached, so the proof survives in test-results/ even with
      // the list reporter.
      if (baseline) {
        // The code and its countdown change every run, so mask them. The
        // run-scoped project id is deliberately NOT masked: it is a few
        // characters of text, far inside the 7% pixel budget the suite already
        // allows, and masking the row would also cover the veil's label.
        await expect(page).toHaveScreenshot(name, {
          fullPage: true,
          mask: [page.getByTestId('sync-code'), page.getByTestId('sync-expiry')],
        })
      }
      const path = testInfo.outputPath(name)
      await page.screenshot({ path, fullPage: true })
      await testInfo.attach(name, { path, contentType: 'image/png' })
    }
    const card = () => page.getByTestId(`project-card-${projectId}`)
    // freshPoll resolves on the next document's first status request, so the
    // reload assertions below are about server truth rather than a timeout.
    const freshPoll = () => page.waitForResponse((r) => r.request().method() === 'GET' && r.url().endsWith('/sync'))
    const openSync = async () => {
      await page.getByTestId(`project-menu-${projectId}`).click()
      await page.getByRole('menuitem', { name: 'Work Sync' }).click()
      await expect(page.getByTestId('work-sync-dialog')).toBeVisible()
      await expect(page.getByTestId('sync-code')).toHaveText(/^[0-9A-HJKMNP-TV-Z]{4}(-[0-9A-HJKMNP-TV-Z]{4}){3}$/)
    }

    // Home probes GitHub before showing projects; e2e has no GitHub account.
    await mockSshGate(page)
    await page.goto('/app')
    await expect(card()).toBeVisible({ timeout: ciTimeout(30_000) })

    // ── modal: code, expiry, command, and maybe-upload/download copy ──
    await openSync()
    await expect(page.getByTestId('sync-expiry')).toHaveText(/Expires in \d:\d\d/)
    await expect(page.getByTestId('sync-command')).toContainText('/sync.sh')
    await expect(page.getByTestId('work-sync-dialog')).toContainText('Upload overwrites pcoder')
    // The user acknowledges the agent caveat on both sides: here and in the
    // laptop's confirmation prompt (asserted below).
    await expect(page.getByTestId('sync-agent-warning')).toContainText('Stop agent turns in this project first')
    await expect(page.getByTestId('sync-status')).toHaveText('Waiting for your laptop…')
    await shot('work-sync-modal.png', true)

    // ── 1. Download: pcoder → laptop. The project locks while it runs. ──
    const download = startClient(['--project', projectId, '--code', (await syncCode(request)).code,
      '--dir', localA, '--direction', 'download'])
    await download.waitFor('Continue?')
    await expect(page.getByTestId('project-sync-lock')).toBeVisible({ timeout: ciTimeout(10_000) })
    await expect(page.getByTestId('project-sync-lock')).toHaveAttribute('data-status', 'downloading')
    await expect(page.getByTestId('sync-status')).toHaveText(/Downloading…/)
    await expect(page.getByTestId(`project-open-${projectId}`)).toBeDisabled()
    // The lock shuts every way in, not just the row's main button: the menu
    // trigger is disabled too, so a delete or a harness install cannot race a
    // live transfer. The routed view locks as well.
    await expect(page.getByTestId(`project-menu-${projectId}`)).toBeDisabled()
    // Shoot the row, not the dialog: the veil and the disabled controls are
    // the subject. The live status line is asserted above, before closing.
    await page.keyboard.press('Escape')
    await shot('work-sync-downloading-lock.png', true)
    // A reload must re-derive the lock: the transfer is pcoder's state, not
    // the tab's. The veil and both disabled controls have to come back from a
    // fresh document, after its first status response.
    const lockedPoll = freshPoll()
    await page.reload()
    await lockedPoll
    await expect(card()).toHaveAttribute('data-sync', 'downloading')
    await expect(page.getByTestId(`project-open-${projectId}`)).toBeDisabled()
    await expect(page.getByTestId(`project-menu-${projectId}`)).toBeDisabled()
    await page.goto(terminalUrl(projectId, 'main'))
    await expect(page.getByTestId('project-sync-lock')).toBeVisible({ timeout: ciTimeout(10_000) })
    download.confirm()
    await download.waitForExit()
    expect(download.output).toContain('Downloaded.')
    await expect(page.getByTestId('project-sync-lock')).toHaveCount(0, { timeout: ciTimeout(10_000) })
    expect(await syncStatus(request)).toBe('idle')

    // ── 2. Change the laptop copy: one edit, one new file. ──
    writeFileSync(join(localA, 'hello.txt'), 'changed on laptop\n')
    writeFileSync(join(localA, 'notes.txt'), 'new on laptop\n')

    // pcoder is still clean before the upload — the "before" shot.
    await openDiff(page, projectId)
    await expect(page.getByTestId('diff-file')).toHaveCount(0)
    await shot('work-sync-diff-before.png') // artifact only: diff visuals live in diff.visual.spec.ts
    await page.goto('/app')
    await expect(card()).toBeVisible()
    await openSync()

    // ── 3. Upload: laptop → pcoder. Same lock, opposite direction. ──
    const upload = startClient(['--project', projectId, '--code', (await syncCode(request)).code,
      '--dir', localA, '--direction', 'upload'])
    await upload.waitFor('Continue?')
    expect(upload.output).toContain('1 added, 1 files will be overwritten')
    // The laptop's confirmation prompt is where the user acknowledges the
    // agent caveat — pcoder cannot detect a running turn for them.
    expect(upload.output).toContain('Stop agent turns in this project first')
    await expect(page.getByTestId('project-sync-lock')).toBeVisible({ timeout: ciTimeout(10_000) })
    await expect(page.getByTestId('project-sync-lock')).toHaveAttribute('data-status', 'uploading')
    await expect(page.getByTestId('sync-status')).toHaveText(/Uploading…/)
    await page.keyboard.press('Escape')
    await shot('work-sync-uploading-lock.png', true)
    upload.confirm()
    await upload.waitForExit()
    expect(upload.output).toContain('Uploaded. 2 updated, 0 deleted on pcoder.')
    await expect(page.getByTestId('project-sync-lock')).toHaveCount(0, { timeout: ciTimeout(10_000) })
    // Unlocked means interactive again, not merely unpainted: both ways into
    // the project are live and the server reports no transfer.
    await expect(page.getByTestId(`project-open-${projectId}`)).toBeEnabled()
    await expect(page.getByTestId(`project-menu-${projectId}`)).toBeEnabled()
    expect(await syncStatus(request)).toBe('idle')
    // ...and a fresh document agrees, rather than trusting the tab that ran
    // the transfer: no stuck veil, no stuck lock on the server.
    const idlePoll = freshPoll()
    await page.reload()
    await idlePoll
    await expect(card()).toHaveAttribute('data-sync', 'idle')
    await expect(page.getByTestId(`project-open-${projectId}`)).toBeEnabled()

    // ── 4. Verify the pcoder side through the app's own file API. ──
    expect(await readProjectFile(request, projectId, 'hello.txt')).toBe('changed on laptop')
    expect(await readProjectFile(request, projectId, 'notes.txt')).toBe('new on laptop')
    await openDiff(page, projectId)
    await expect(page.locator('[data-testid="diff-file"][data-path="hello.txt"]')).toBeVisible()
    await shot('work-sync-diff-after.png')

    // ── 4b. Settled, both sides, no leftovers. ──
    // pcoder's tree IS the laptop's tree: one modified tracked file, one new
    // untracked file, and nothing else — no sync temp files, no touched
    // README, no stray backup inside the repo. The laptop mirrors it.
    const want = [' M hello.txt', '?? notes.txt']
    expect(await remotePorcelain(request, projectId)).toEqual(want)
    expect(localPorcelain(localA)).toEqual(want)
    expect(readdirSync(localA).sort()).toEqual(['.git', 'README.md', 'hello.txt', 'notes.txt'])

    // Converged: re-downloading the same tree moves nothing, so both sides
    // hold identical trees instead of drifting copies.
    const noop = startClient(['--project', projectId, '--code', (await syncCode(request)).code,
      '--dir', localA, '--direction', 'download'])
    await noop.waitFor('Continue?')
    expect(noop.output).toContain('0 files will be overwritten')
    noop.confirm()
    await noop.waitForExit()
    expect(noop.output).toContain('Downloaded. 0 updated, 0 deleted here.')

    // ── 5. Download into a second clone through the real bootstrap. ──
    const boot = runBootstrap(['--project', projectId, '--code', (await syncCode(request)).code,
      '--dir', localB, '--direction', 'download', '--yes'])
    expect(boot).toContain('Downloaded.')
    expect(readFileSync(join(localB, 'hello.txt'), 'utf8')).toBe('changed on laptop\n')
    expect(readFileSync(join(localB, 'notes.txt'), 'utf8')).toBe('new on laptop\n')

    // ── 6. Error state, surfaced in the dialog word for word. ──
    await page.goto('/app')
    await expect(card()).toBeVisible()
    await page.route('**/api/projects/*/sync/code', (route) =>
      route.fulfill({
        status: 409,
        contentType: 'application/json',
        body: JSON.stringify({ error: { code: 'sync_running', message: 'Sync already running for this project.\nIf nothing seems to be happening, just wait — it sorts itself out within 10 minutes, then generate a new code.' } }),
      }))
    await page.getByTestId(`project-menu-${projectId}`).click()
    await page.getByRole('menuitem', { name: 'Work Sync' }).click()
    await expect(page.getByTestId('sync-error')).toHaveText(/Sync already running for this project/)
    await shot('work-sync-error.png', true)
  })


  async function syncCode(request: APIRequestContext): Promise<{ code: string }> {
    const res = await request.post(`/api/projects/${projectURL(projectId)}/sync/code`)
    expect(res.status()).toBe(200)
    return (await res.json()) as { code: string }
  }

  // syncStatus reads the live transfer state the UI polls.
  async function syncStatus(request: APIRequestContext): Promise<string> {
    const res = await request.get(`/api/projects/${projectURL(projectId)}/sync`)
    expect(res.ok()).toBeTruthy()
    return ((await res.json()) as { status: string }).status
  }

  // startClient drives the static client with a live stdin so the test can
  // hold the confirmation prompt open and observe the lock.
  function startClient(args: string[]): {
    output: string; waitFor: (text: string) => Promise<void>; confirm: () => void; waitForExit: () => Promise<void>
  } {
    const child: ChildProcessWithoutNullStreams = spawn(BIN, ['--server', BACKEND, ...args], {
      env: { ...process.env, HOME },
      stdio: ['pipe', 'pipe', 'pipe'],
    })
    const run = {
      output: '',
      child,
      exited: undefined as number | null | undefined,
      waiters: [] as { text: string; resolve: () => void }[],
    }
    const onData = (b: Buffer) => {
      run.output += b.toString()
      for (const w of [...run.waiters]) {
        if (run.output.includes(w.text)) {
          run.waiters = run.waiters.filter((x) => x !== w)
          w.resolve()
        }
      }
    }
    child.stdout.on('data', onData)
    child.stderr.on('data', onData)
    const exit = new Promise<void>((res) => child.on('exit', (code) => { run.exited = code; res() }))
    return {
      get output() { return run.output },
      waitFor: (text) => new Promise<void>((res, rej) => {
        if (run.output.includes(text)) return res()
        const timer = setTimeout(() => rej(new Error(`timed out waiting for ${JSON.stringify(text)}\n${run.output}`)), ciTimeout(60_000))
        run.waiters.push({ text, resolve: () => { clearTimeout(timer); res() } })
      }),
      confirm: () => child.stdin.write('y\n'),
      waitForExit: async () => {
        await exit
        if (run.exited !== 0) throw new Error(`client exited ${run.exited}\n${run.output}`)
      },
    }
  }
})

// runBootstrap runs the documented laptop command: curl the launcher and
// pipe it to sh. --yes keeps it non-interactive (stdin belongs to the pipe).
function runBootstrap(args: string[]): string {
  const command = `curl -fsSL ${BACKEND}/sync.sh | sh -s -- ${args.map(quote).join(' ')}`
  return execFileSync('sh', ['-c', command], {
    env: { ...process.env, HOME, XDG_CACHE_HOME: join(HOME, '.cache') },
    encoding: 'utf8',
    timeout: ciTimeout(180_000),
  })
}

async function readProjectFile(request: APIRequestContext, projectId: string, path: string): Promise<string> {
  const res = await request.get(`/api/projects/${projectURL(projectId)}/file?path=${encodeURIComponent(path)}&start=1&end=5`)
  expect(res.ok()).toBeTruthy()
  return ((await res.json()) as { content: string }).content.trim()
}

// shInProject runs a shell command in the running project container and
// returns its raw stdout — the test's direct window into the remote side
// (git state, tmux panes). The result status is asserted so a skipped or
// failed exec can never masquerade as empty output.
async function shInProject(request: APIRequestContext, projectId: string, command: string): Promise<string> {
  const res = await request.post('/api/projects/exec', { data: { projectIds: [projectId], command } })
  expect(res.ok()).toBeTruthy()
  const results = ((await res.json()) as { results: { status: string; detail: string }[] }).results
  expect(results[0]?.status, results[0]?.detail).toBe('ok')
  return results[0]?.detail ?? ''
}

// remotePorcelain is the container's `git status --porcelain`, byte-intact.
// ExecCommand trims its output (eating the leading XY space of a first
// entry), so a sentinel line absorbs the trim — the same trick the Git tab
// uses. exec starts in the container root, so cd into the workspace first
// (session.go: /workspace/repo, falling back to /workspace).
async function remotePorcelain(request: APIRequestContext, projectId: string): Promise<string[]> {
  const raw = await shInProject(request, projectId,
    'cd /workspace/repo 2>/dev/null || cd /workspace; echo SENTINEL; git status --porcelain')
  return porcelain(raw.split('\n').slice(1).join('\n'))
}

// localPorcelain is remotePorcelain for the laptop's clone.
function localPorcelain(dir: string): string[] {
  return porcelain(execFileSync('git', ['status', '--porcelain'], { cwd: dir, encoding: 'utf8' }))
}

function porcelain(out: string): string[] {
  return out.split('\n').map((l) => l.trimEnd()).filter(Boolean).sort()
}

// Home probes GitHub before it shows projects; e2e has no GitHub account, so
// every spec that needs project rows on /app stubs the gate open.
function mockSshGate(page: Page): Promise<Disposable> {
  return page.route('**/api/ssh/test', (route) =>
    route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, user: 'octocat' }) }))
}

// openDiff lands on the project terminal and opens the Git tab, where an
// uncommitted change on pcoder is visible.
async function openDiff(page: Page, projectId: string) {
  await page.goto(`/projects/${encodeURIComponent(projectId)}/terminal/main`)
  await expect(page.locator('.xterm-screen')).toBeVisible({ timeout: ciTimeout(30_000) })
  await page.getByTestId('tab-diff').click()
  await expect(page.getByTestId('diff-tab')).toBeVisible({ timeout: ciTimeout(15_000) })
}

function clone(dir: string) {
  execFileSync('git', ['clone', '-q', `git://127.0.0.1:${GIT_PORT}/e2e/${RUN_SLUG}-${SLOT}`, dir], {
    stdio: 'ignore',
    timeout: ciTimeout(60_000),
  })
}

function quote(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`
}
