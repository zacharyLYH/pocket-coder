// Route-mocked fixtures for the Git tab suite: canned status/diff/log/
// identity/branches + AI + ship endpoints. No engine, no projects —
// specs mock the API and drive the real TerminalView (the same pattern
// as nerdy.fixtures.ts).
//
// Glob gotcha (see skills/playwright-route-mocks): `*` does not cross
// `/`, so `**/api/projects/*/git/**` is used for sub-paths.
import type { Page } from '@playwright/test'

export const PROJECT = 'demo/git'

const FILE_DIFF_U3 = `diff --git a/hello.txt b/hello.txt
index 1111111..2222222 100644
--- a/hello.txt
+++ b/hello.txt
@@ -1,3 +1,4 @@
 line1
 line2
 line3
+world
`

const FILE_DIFF_U10 = `diff --git a/hello.txt b/hello.txt
index 1111111..2222222 100644
--- a/hello.txt
+++ b/hello.txt
@@ -1,10 +1,11 @@
 ctx1
 ctx2
 ctx3
 ctx4
 ctx5
 line1
 line2
 line3
+world
 ctx9
 ctx10
`

export const STATUS = {
  branch: 'main',
  files: [
    { path: 'hello.txt', staged: ' ', unstaged: 'M', stagedAdd: 0, stagedDel: 0, unstagedAdd: 1, unstagedDel: 0, binary: false },
  ],
  upstream: { name: 'origin/main', ahead: 0, behind: 0 },
  unborn: false,
}

export const STATUS_NO_UPSTREAM = { ...STATUS, upstream: null }

export const IDENTITY = { name: 'Zac', email: 'z@x.io' }
export const IDENTITY_EMPTY = { name: '', email: '' }

export const BRANCHES = {
  current: 'main',
  detached: false,
  local: [
    { name: 'main', relativeTime: '2 hours ago' },
    { name: 'feature/other', relativeTime: '3 days ago' },
  ],
  remote: [{ name: 'main', relativeTime: '2 hours ago' }],
}

// STATE is a mutable test store so POST handlers (commit, push) can
// mutate what GET /git/status answers — the spec drives real flows:
// commit cleans the tree with 1 ahead, push drains it back to 0.
export const STATE = { committed: false, ahead: 0 }

export function resetGitState() {
  STATE.committed = false
  STATE.ahead = 0
}

export async function mockGit(page: Page, opts: { identityEmpty?: boolean; noUpstream?: boolean; switchConflict?: boolean; pushFail?: string; unborn?: boolean } = {}) {
  const status = () => {
    if (opts.unborn) return { branch: '', files: [], upstream: null, unborn: true }
    const base = opts.noUpstream ? STATUS_NO_UPSTREAM : STATUS
    const upstream = base.upstream ? { ...base.upstream, ahead: STATE.ahead } : null
    return STATE.committed ? { ...base, upstream, files: [] } : { ...base, upstream }
  }

  await page.route('**/api/ai/config', (r) =>
    r.fulfill({ json: { baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', configured: true } }))
  await page.route('**/api/projects/*/sessions*', (r) =>
    r.fulfill({ json: r.request().method() === 'GET' ? { sessions: [{ name: 'main' }] } : { name: 'main' } }))
  await page.route('**/api/projects/*/harnesses', (r) => r.fulfill({ json: { harnesses: [] } }))
  await page.route('**/api/projects/*/codemap/threads*', (r) =>
    r.fulfill({ json: r.request().method() === 'GET' ? { threads: [], runningThreadId: null } : {} }))

  await page.route('**/api/projects/*/git/status', (r) =>
    r.fulfill({ json: STATE.committed ? { ...status(), files: [] } : status() }))

  await page.route('**/api/projects/*/git/stage', (r) => r.fulfill({ json: { ok: true } }))
  await page.route('**/api/projects/*/git/unstage', (r) => r.fulfill({ json: { ok: true } }))
  await page.route('**/api/projects/*/git/stage-hunk', (r) => r.fulfill({ json: { ok: true } }))

  await page.route('**/api/projects/*/git/diff*', (r) => {
    const u = new URL(r.request().url())
    const un = u.searchParams.get('context') ?? '3'
    const diff = un === '10' ? FILE_DIFF_U10 : un === '3' ? FILE_DIFF_U3 : FILE_DIFF_U10
    return r.fulfill({ json: { path: u.searchParams.get('path'), diff, truncated: false, oldContent: 'line1\nline2\nline3', newContent: 'line1\nline2\nline3\nworld', binary: false } })
  })

  await page.route('**/api/projects/*/git/identity', (r) => {
    if (r.request().method() === 'POST') {
      const body = r.request().postDataJSON() as { name: string; email: string }
      return r.fulfill({ json: { ok: true, ...body } })
    }
    return r.fulfill({ json: opts.identityEmpty ? IDENTITY_EMPTY : IDENTITY })
  })
  await page.route('**/api/projects/*/git/branches', (r) => r.fulfill({ json: BRANCHES }))
  await page.route('**/api/projects/*/git/switch', (r) => {
    if (opts.switchConflict) {
      return r.fulfill({ status: 409, json: { error: 'working tree has uncommitted changes — commit or stage first: hello.txt' } })
    }
    return r.fulfill({ json: { ok: true, branch: (r.request().postDataJSON() as { branch: string }).branch } })
  })

  await page.route('**/api/projects/*/git/commit', (r) => {
    STATE.committed = true
    STATE.ahead = 1
    return r.fulfill({ json: { ok: true, commit: 'abc1234', branch: 'main' } })
  })
  await page.route('**/api/projects/*/git/push', (r) => {
    if (opts.pushFail) return r.fulfill({ status: 502, json: { error: opts.pushFail } })
    STATE.ahead = 0
    return r.fulfill({ json: { ok: true, branch: 'main', output: 'To github.com: pushed\n' } })
  })
  await page.route('**/api/projects/*/git/pull', (r) => r.fulfill({ json: { ok: true, output: 'Already up to date.\n' } }))

  await page.route('**/api/projects/*/git/commit-message', (r) =>
    r.fulfill({ json: { subject: 'feat: add world greeting', body: 'Appends a greeting line to hello.txt.' } }))
  await page.route('**/api/projects/*/git/pr-body', (r) =>
    r.fulfill({ json: { title: 'Add world greeting', body: '## What\nAdds a greeting line.\n\n## Why\nDemo.\n\n## How\nAppend to hello.txt.' } }))
  await page.route('**/api/projects/*/git/explain', (r) =>
    r.fulfill({ json: { threadId: 'thre-1234', threadTitle: 'Changes walkthrough' } }))
}

export function terminalUrl(id: string, session: string): string {
  return `/projects/${encodeURIComponent(id)}/terminal/${encodeURIComponent(session)}`
}
