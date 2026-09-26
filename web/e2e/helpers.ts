import { readFileSync } from 'node:fs'
import { expect, type Page, type APIRequestContext } from '@playwright/test'

import { GIT_PORT, RUN_SLUG, SERVER_LOG } from './env'

// Shared plumbing for the real-backend e2e suite: real PIN login, and
// per-test cleanup so order never matters (the stack data dir only resets
// between runs — see playwright.config.ts).

export const LOGIN_EMAIL = 'me@example.com'

// e2eRepo returns the fixture repo URL for a slot: one repo is one project
// (ids are owner/repo), so tests holding N projects at once use slots
// 1..N. Served by the per-run git daemon (global-setup.ts) — hermetic,
// instant, and namespaced per run so parallel groups sharing one engine
// never collide.
export function e2eRepo(slot = 1): string {
  return `git://host.docker.internal:${GIT_PORT}/e2e/${RUN_SLUG}-${slot}`
}

// e2eRepoID is the project id a slot's repo creates.
export function e2eRepoID(slot = 1): string {
  return `e2e/${RUN_SLUG}-${slot}`
}

// projectURL escapes an owner/repo id for API paths and page URLs.
export function projectURL(id: string): string {
  return encodeURIComponent(id)
}

export function terminalUrl(id: string, session: string): string {
  return `/projects/${projectURL(id)}/terminal/${encodeURIComponent(session)}`
}

// login drives the real email + console-mailer PIN flow end to end.
export async function login(page: Page) {
  await page.goto('/')
  const pinOffset = logSize()
  await page.getByPlaceholder('you@example.com').fill(LOGIN_EMAIL)
  await page.getByRole('button', { name: 'Send code' }).click()
  await expect(page.getByPlaceholder('6-digit PIN')).toBeVisible()
  await page.getByPlaceholder('6-digit PIN').fill(waitForPin(pinOffset))
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByText(`Welcome, ${LOGIN_EMAIL}`)).toBeVisible()
}

// engineUp reports whether the backend can reach its Docker engine; the
// server maps an unreachable engine to 500 on project ops, a healthy one
// answers 200 even with zero projects.
export async function engineUp(request: APIRequestContext): Promise<boolean> {
  const res = await request.get('/api/projects')
  return res.status() !== 500
}

// deleteAllProjects removes every project (data + volumes + metadata).
// Call it before tests that need a clean slate AND in `finally`, so a
// failed test never leaks projects into later tests or the next run.
// Deletions retry (the engine can hiccup under parallel load) and then
// throw: a silent cleanup failure is how invisible volume orphans happen.
export async function deleteAllProjects(request: APIRequestContext): Promise<void> {
  const res = await request.get('/api/projects')
  // 500 means the engine is down (callers skip on engineUp); anything else
  // failing must throw — with deterministic owner/repo ids, silently
  // keeping a project poisons every later create of the same repo.
  if (!res.ok()) {
    if (res.status() === 500) return
    throw new Error(`deleteAllProjects: list projects → ${res.status()}`)
  }
  const failures: string[] = []
  for (const p of ((await res.json()) as { projects: { id: string }[] }).projects) {
    let ok = false
    for (let attempt = 0; attempt < 3 && !ok; attempt++) {
      if (attempt > 0) await new Promise((r) => setTimeout(r, 1000))
      ok = (await request.delete(`/api/projects/${projectURL(p.id)}?scope=all`)).ok()
    }
    if (!ok) failures.push(p.id)
  }
  if (failures.length > 0) {
    throw new Error(`deleteAllProjects failed for: ${failures.join(', ')} — retrying keeps them listed, investigate the engine`)
  }
}

// resetHarnessRegistry removes every non-builtin harness. The registry is
// global and specs may add plugins (e.g. orchestration's combo agent), so
// visual tests that capture the harness card pin it back to the eight
// builtins first — otherwise full-page home shots shift with registry state.
const BUILTINS = ['terminal', 'opencode', 'codex', 'claude', 'freebuff', 'cline', 'pi', 'kiro']

export async function resetHarnessRegistry(request: APIRequestContext): Promise<void> {
  const res = await request.get('/api/harnesses')
  if (!res.ok()) return
  for (const h of ((await res.json()) as { harnesses: { id: string }[] }).harnesses) {
    if (!BUILTINS.includes(h.id)) {
      await request.delete(`/api/harnesses/${h.id}`)
    }
  }
}

// ensureFakeHarness registers the instant fake CLI e2e uses as its install
// vehicle: a local printf script, so installs exercise the real pipeline
// with no network. Delete-then-create keeps it idempotent across specs
// sharing one backend.
export const FAKE_HARNESS_ID = 'e2e-fake'
export const FAKE_HARNESS_NAME = 'E2E Fake'

export async function ensureFakeHarness(request: APIRequestContext): Promise<void> {
  await request.delete(`/api/harnesses/${FAKE_HARNESS_ID}`)
  const res = await request.post('/api/harnesses', {
    data: {
      name: FAKE_HARNESS_NAME,
      command: 'e2efake',
      install: `printf '#!/bin/sh\\nif [ $# -gt 0 ]; then echo "e2efake 1.0"; exit 0; fi\\nexec bash\\n' > /usr/local/bin/e2efake && chmod +x /usr/local/bin/e2efake`,
    },
  })
  if (res.status() !== 201) throw new Error(`ensureFakeHarness failed: ${res.status()}`)
}

// deleteAllSSHKeys likewise empties the key registry.
export async function deleteAllSSHKeys(request: APIRequestContext): Promise<void> {
  const res = await request.get('/api/ssh-keys')
  if (!res.ok()) return
  for (const k of ((await res.json()) as { keys: { fingerprint: string }[] }).keys) {
    await request.delete(`/api/ssh-keys/${encodeURIComponent(k.fingerprint)}`)
  }
}

function logSize(): number {
  try {
    return readFileSync(SERVER_LOG, 'utf8').length
  } catch {
    return 0
  }
}

function waitForPin(afterOffset: number): string {
  for (let i = 0; i < 50; i++) {
    try {
      const freshLog = readFileSync(SERVER_LOG, 'utf8').slice(afterOffset)
      const matches = [...freshLog.matchAll(new RegExp(`login PIN for ${LOGIN_EMAIL}: (\\d{6})`, 'g'))]
      if (matches.length > 0) return matches[matches.length - 1][1]
    } catch {
      // log not flushed yet
    }
    Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 100)
  }
  throw new Error(`no PIN found in ${SERVER_LOG} — does the console mailer print it?`)
}

// ensureCloneForm lands on home with the Clone disclosure open: the form
// hides inside a closed <details> once a project exists, and inputs in a
// closed disclosure are invisible to Playwright (fill would wait forever).
// Fails fast when Git is not configured: the Clone button is permanently
// disabled then (see ProjectsCard gitConfigured gating), so proceeding
// would spin on an unclickable button until timeout. A stale state.json
// without the git block is the usual cause — the e2e seeds carry one.
async function ensureCloneForm(page: Page): Promise<void> {
  if ((await page.getByPlaceholder(/clone URL/i).count()) === 0) {
    await page.goto('/')
  }
  const input = page.getByPlaceholder(/clone URL/i)
  if (!(await input.first().isVisible())) {
    await page.locator('summary', { hasText: 'Clone a repo' }).click()
  }
  await expect(input.first()).toBeVisible({ timeout: 10_000 })
  if ((await page.getByTestId('git-setup-hint').count()) > 0) {
    throw new Error(
      'Git not configured (git-setup-hint visible): Clone project is disabled. ' +
        'The stack booted from a state.json without the git block — wipe the data dir and reboot from state.seed.json.',
    )
  }
}
// createProject clones a fixture repo via the API and returns its id
// (owner/repo). The repo defaults to slot 1; tests holding several
// projects at once pass distinct slots.
export async function createProject(request: APIRequestContext, repoUrl = e2eRepo(1)): Promise<string> {
  let res = await request.post('/api/projects', { data: { repoUrl } })
  if (res.status() === 409) {
    const body = await res.text().catch(() => '')
    if (/git not configured/i.test(body)) {
      throw new Error(
        `createProject: backend reports git not configured — the stack booted from a state.json without the git block. Body: ${body}`,
      )
    }
  }
  for (let round = 0; res.status() === 409 && round < 3; round++) {
    // Same-id project leaked by an earlier cleanup failure (ids are this
    // run's fixture slots, so it is always ours): drop everything listed
    // and retry. Deletes can fail under engine load, so retry the whole
    // recovery instead of assuming one pass worked.
    const probe = await request.get('/api/projects')
    const ids = ((await probe.json()) as { projects: { id: string }[] }).projects.map((p) => p.id)
    for (const id of ids) {
      await request.delete(`/api/projects/${projectURL(id)}?scope=all`)
    }
    res = await request.post('/api/projects', { data: { repoUrl } })
  }
  expect(res.status()).toBe(201)
  const { id } = (await res.json()) as { id: string }
  return id
}

// fetchEvents reads the whole audit log through the paginated API. Specs
// must NOT read $DATA_DIR/events.log off disk: inside the compose stack the
// server runs as root, so on a Linux CI bind mount that file is created
// root-owned 0600 and the runner user gets EACCES reading it. The request
// fixture carries the session cookie, so the authed API just works.
export async function fetchEvents(request: APIRequestContext): Promise<{ type: string }[]> {
  const out: { type: string }[] = []
  let after = 0
  for (;;) {
    const res = await request.get(`/api/events?after=${after}`)
    expect(res.ok()).toBeTruthy()
    const { events } = (await res.json()) as { events: { id: number; type: string }[] | null }
    // The handler serializes a nil slice as null on the final empty page.
    const page = events ?? []
    if (page.length === 0) break
    out.push(...page)
    after = page[page.length - 1].id
  }
  return out
}

// createProjectViaUI creates a project through the real home-page form,
// exactly as a user would, then returns its id once running.
export async function createProjectViaUI(
  page: Page,
  request: APIRequestContext,
  repoUrl: string,
  expectedID: string,
): Promise<string> {
  // Fail fast on a stale backend: without the git seed every create 409s
  // with "git not configured" and the UI leaves Clone disabled, which
  // otherwise surfaces as a timeout clicking Clone project.
  const gitRes = await request.get('/api/git/identities')
  if (gitRes.ok()) {
    const gitBody = (await gitRes.json()) as { identities?: unknown[] }
    if ((gitBody.identities ?? []).length === 0) {
      throw new Error(
        'Git not configured on the backend (/api/git/identities empty): ' +
          'the stack booted from a state.json without the git block — wipe the data dir and reboot from state.seed.json.',
      )
    }
  }
  const countNamed = async (): Promise<number> => {
    const res = await request.get('/api/projects')
    if (!res.ok()) return 0
    return ((await res.json()) as { projects: { id: string }[] }).projects.filter(
      (p) => p.id === expectedID,
    ).length
  }
  let before = await countNamed()
  if (before > 0) {
    // A same-id project is already listed (a previous cleanup failed or
    // was skipped): remove it first so this create is not a 409 loop.
    // Ids are this run's namespaced fixture slots, so this is always ours.
    await request.delete(`/api/projects/${projectURL(expectedID)}?scope=all`)
    before = await countNamed()
  }

  // Under CI load the Vite dev client can drop its websocket and reload the
  // page mid-submit, replacing the form ("element(s) not found") and losing
  // the create. A lost create is harmless — nothing was made — so resubmit
  // The POST outcome is the source of truth here, not the list count:
  // the server lists the record while provisioning is still in flight
  // and silently rolls back on failure, so "listed" alone can exit the
  // poll before the create resolves. A SUCCESSFUL create clears the form;
  // a FAILED attempt keeps the values and shows the error. Both are plain
  // DOM reads — unlike isEnabled neither blocks on actionability checks.
  for (let attempt = 0; attempt < 5 && (await countNamed()) <= before; attempt++) {
    await ensureCloneForm(page)
    await page.getByPlaceholder(/clone URL/i).fill(repoUrl)
    await page.getByRole('button', { name: 'Clone project' }).click()
    await expect(async () => {
      const urlValue = await page.getByPlaceholder(/clone URL/i).inputValue()
      const created = urlValue === '' && (await countNamed()) > before
      const failed = urlValue !== '' && (await page.getByTestId('clone-error').count()) > 0
      expect(created || failed).toBe(true)
    }).toPass({ timeout: 120_000 })
  }
  if ((await countNamed()) <= before) {
    throw new Error(`project ${expectedID} not created after retries`)
  }

  await page.reload()
  await expect(page.getByTestId(`project-card-${expectedID}`)).toHaveCount(before + 1, { timeout: 10_000 })
  const orderRes = await request.get('/api/projects')
  const orderBody = (await orderRes.json()) as { projects: { id: string }[] }
  const created = orderBody.projects.find((p) => p.id === expectedID)
  if (!created) throw new Error(`project ${expectedID} not found after create`)
  await waitForRunning(request, created.id)
  return created.id
}

// waitForRunning polls a project until its container reports running.
export async function waitForRunning(request: APIRequestContext, id: string): Promise<void> {
  for (let i = 0; i < 60; i++) {
    const res = await request.get(`/api/projects/${projectURL(id)}`)
    if (res.ok() && ((await res.json()) as { status: string }).status === 'running') return
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`project ${id} never reached running`)
}
