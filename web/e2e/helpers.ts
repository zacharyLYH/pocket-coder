import { readFileSync } from 'node:fs'
import { expect, type Page, type APIRequestContext } from '@playwright/test'

import { SERVER_LOG } from './env'

// Shared plumbing for the real-backend e2e suite: real PIN login, and
// per-test cleanup so order never matters (the stack data dir only resets
// between runs — see playwright.config.ts).

export const LOGIN_EMAIL = 'me@example.com'

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
  if (!res.ok()) return
  const failures: string[] = []
  for (const p of ((await res.json()) as { projects: { id: string }[] }).projects) {
    let ok = false
    for (let attempt = 0; attempt < 3 && !ok; attempt++) {
      if (attempt > 0) await new Promise((r) => setTimeout(r, 1000))
      ok = (await request.delete(`/api/projects/${p.id}?scope=all`)).ok()
    }
    if (!ok) failures.push(p.id)
  }
  if (failures.length > 0) {
    throw new Error(`deleteAllProjects failed for: ${failures.join(', ')} — retrying keeps them listed, investigate the engine`)
  }
}

// resetHarnessRegistry removes every non-builtin harness. The registry is
// global and specs may add plugins (e.g. orchestration's combo agent), so
// visual tests that capture the harness card pin it back to the six
// builtins first — otherwise full-page home shots shift with registry state.
const BUILTINS = ['terminal', 'opencode', 'freebuff', 'cline', 'vi-demo', 'crasher-demo']

export async function resetHarnessRegistry(request: APIRequestContext): Promise<void> {
  const res = await request.get('/api/harnesses')
  if (!res.ok()) return
  for (const h of ((await res.json()) as { harnesses: { id: string }[] }).harnesses) {
    if (!BUILTINS.includes(h.id)) {
      await request.delete(`/api/harnesses/${h.id}`)
    }
  }
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

// createBlankProject makes an empty project via the API and returns its id.
export async function createBlankProject(request: APIRequestContext): Promise<string> {
  const res = await request.post('/api/projects', { data: {} })
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
  expectedName: string,
): Promise<string> {
  // Count projects already named expectedName, so the helper also works for
  // the repeated "untitled" case (it must create a NEW one, not see the old).
  const countNamed = async (): Promise<number> => {
    const res = await request.get('/api/projects')
    if (!res.ok()) return 0
    return ((await res.json()) as { projects: { name: string }[] }).projects.filter(
      (p) => p.name === expectedName,
    ).length
  }
  const before = await countNamed()

  // Under CI load the Vite dev client can drop its websocket and reload the
  // page mid-submit, replacing the form ("element(s) not found") and losing
  // the create. A lost create is harmless — nothing was made — so resubmit
  // until the project actually exists. Each POST is synchronous server-side
  // and the count check runs before every resubmit, so a slow-but-successful
  // create is never duplicated.
  for (let attempt = 0; attempt < 5 && (await countNamed()) <= before; attempt++) {
    if ((await page.getByPlaceholder(/Repo URL/).count()) === 0) {
      await page.goto('/')
    }
    await page.getByPlaceholder(/Repo URL/).fill(repoUrl)
    await page.getByRole('button', { name: 'Create project' }).click()
    // The button is disabled ("Creating…") while the POST is in flight and
    // comes back enabled when it lands; a page reload also lands here.
    await expect(page.getByRole('button', { name: /Create project|Creating…/ })).toBeVisible({
      timeout: 120_000,
    })
    await expect(page.getByRole('button', { name: 'Create project' })).toBeEnabled({ timeout: 120_000 })
  }
  if ((await countNamed()) <= before) {
    throw new Error(`project ${expectedName} not created after retries`)
  }

  await page.reload()
  // Count-aware: several specs create duplicate names ('untitled'), so a
  // bare toBeVisible strict-violates on 2+ matches. The create loop above
  // guarantees exactly before+1 once the reload settles.
  await expect(page.getByText(expectedName)).toHaveCount(before + 1, { timeout: 10_000 })
  const orderRes = await request.get('/api/projects')
  const orderBody = (await orderRes.json()) as { projects: { id: string; name: string }[] }
  const created = orderBody.projects.find((p) => p.name === expectedName)
  if (!created) throw new Error(`project ${expectedName} not found after create`)
  await waitForRunning(request, created.id)
  return created.id
}

// waitForRunning polls a project until its container reports running.
export async function waitForRunning(request: APIRequestContext, id: string): Promise<void> {
  for (let i = 0; i < 60; i++) {
    const res = await request.get(`/api/projects/${id}`)
    if (res.ok() && ((await res.json()) as { status: string }).status === 'running') return
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`project ${id} never reached running`)
}
