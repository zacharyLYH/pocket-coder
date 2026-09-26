// Route-mocked fixtures for the Nerdy Stuff suite: 20 mixed observe lines
// (seqs 1..20, incl. one 3-line error group) + stats + activity. No engine,
// no projects — specs mock the API and drive the real TerminalView.
import type { Page } from '@playwright/test'

export const PROJECT = 'demo/nerdy'

type Log = {
  seq: number
  ts: string
  project: string
  trace: string
  source: string
  level: string
  type: string
  msg: string
  attrs?: Record<string, unknown>
}

const ts = (s: number) => `2026-09-17T05:00:${String(s).padStart(2, '0')}.000000000Z`

export const OBSERVE_LOGS: Log[] = [
  { seq: 1, ts: ts(1), project: PROJECT, trace: '9f3a2c1d4e5f6a7b', source: 'server', level: 'info', type: 'project.create', msg: 'project created' },
  { seq: 2, ts: ts(2), project: PROJECT, trace: '9f3a2c1d4e5f6a7b', source: 'server', level: 'info', type: 'image.build', msg: 'image build started' },
  { seq: 3, ts: ts(3), project: PROJECT, trace: '9f3a2c1d4e5f6a7b', source: 'server', level: 'info', type: 'project.clone', msg: 'clone landed', attrs: { sha: 'abc1234' } },
  { seq: 4, ts: ts(4), project: PROJECT, trace: '9f3a2c1d4e5f6a7b', source: 'server', level: 'info', type: 'project.ready', msg: 'project ready' },
  { seq: 5, ts: ts(5), project: PROJECT, trace: '1111111111111111', source: 'preview', level: 'info', type: 'preview.start', msg: 'Preview started on :3000', attrs: { port: 3000 } },
  { seq: 6, ts: ts(6), project: PROJECT, trace: '1111111111111111', source: 'preview', level: 'info', type: 'preview.open', msg: 'Preview opened' },
  { seq: 7, ts: ts(7), project: PROJECT, trace: '2222222222222222', source: 'server', level: 'warn', type: 'project.reconcile', msg: 'reconciling missing container attempt 1' },
  { seq: 8, ts: ts(8), project: PROJECT, trace: '3333333333333333', source: 'server', level: 'error', type: 'project.clone', msg: 'clone failed sha abc1234def attempt 1' },
  { seq: 9, ts: ts(9), project: PROJECT, trace: '4444444444444444', source: 'server', level: 'error', type: 'project.clone', msg: 'clone failed sha deadbeef99 attempt 2' },
  { seq: 10, ts: ts(10), project: PROJECT, trace: '5555555555555555', source: 'server', level: 'error', type: 'project.clone', msg: 'clone failed sha 00ff11aa22 attempt 3' },
  { seq: 11, ts: ts(11), project: PROJECT, trace: '6666666666666666', source: 'auth', level: 'info', type: 'auth.login', msg: 'login PIN verified' },
  { seq: 12, ts: ts(12), project: PROJECT, trace: '7777777777777777', source: 'system', level: 'info', type: 'project.start', msg: 'container started' },
  { seq: 13, ts: ts(13), project: PROJECT, trace: '8888888888888888', source: 'preview', level: 'info', type: 'preview.navigate', msg: 'Preview went to /about' },
  { seq: 14, ts: ts(14), project: PROJECT, trace: '9999999999999999', source: 'server', level: 'info', type: 'codemap.start', msg: 'ask turn-1 started' },
  { seq: 15, ts: ts(15), project: PROJECT, trace: 'aaaaaaaaaaaaaaaa', source: 'server', level: 'warn', type: 'codemap.busy', msg: 'run rejected: previous run still in flight' },
  { seq: 16, ts: ts(16), project: PROJECT, trace: 'bbbbbbbbbbbbbbbb', source: 'preview', level: 'info', type: 'preview.click', msg: 'Preview clicked #hero' },
  { seq: 17, ts: ts(17), project: PROJECT, trace: 'cccccccccccccccc', source: 'preview', level: 'info', type: 'preview.type', msg: 'Preview typed into #search' },
  { seq: 18, ts: ts(18), project: PROJECT, trace: 'dddddddddddddddd', source: 'server', level: 'info', type: 'project.stop', msg: 'container stopped' },
  { seq: 19, ts: ts(19), project: PROJECT, trace: 'eeeeeeeeeeeeeeee', source: 'server', level: 'info', type: 'project.start', msg: 'container started again' },
  { seq: 20, ts: ts(20), project: PROJECT, trace: 'ffffffffffffffff', source: 'system', level: 'info', type: 'health.ok', msg: 'health check passed from 10.0.0.5' },
]

export const STATS = {
  ts: ts(20), cpuPercent: 12.5, memUsed: 1, memLimit: 2, netRx: 3, netTx: 4,
  blockR: 5, blockW: 6, pids: 7, diskUsed: 1e9, diskTotal: 10e9, state: 'running',
}

export const ACTIVITY = {
  events: [
    { id: 1, time: ts(1), type: 'project.create', data: { id: PROJECT } },
    { id: 2, time: ts(4), type: 'project.ready', data: { id: PROJECT } },
  ],
}

export const ERROR_GROUPS = {
  groups: [{
    key: 'project.clone|clone failed sha <hex> attempt <n>', type: 'project.clone', count: 3,
    firstSeen: ts(8), lastSeen: ts(10), sampleTrace: '3333333333333333',
    sampleMsg: 'clone failed sha abc1234def attempt 1',
  }],
}

// mockNerdy answers every API call the Nerdy panels make, filtering the
// observe fixture server-side like the Go handler (level/source/type/trace/q
// + after/before/limit cursors). Initial tail is the last 10 so Load older shows.
export async function mockNerdy(page: Page) {
  await page.route('**/api/ai/models', (r) => r.fulfill({ json: { models: [] } }))
  await page.route('**/api/projects/*/sessions*', (r) =>
    r.fulfill({ json: r.request().method() === 'GET' ? { sessions: [{ name: 'main' }] } : { name: 'main' } }))
  await page.route('**/api/projects/*/harnesses', (r) => r.fulfill({ json: { harnesses: [] } }))
  await page.route('**/api/events*', (r) => r.fulfill({ json: ACTIVITY }))
  await page.route('**/api/observe/meta', (r) => r.fulfill({ json: {
    auditTypes: ['project.create', 'project.ready', 'session.create', 'terminal.attach', 'preview.start'],
    buildTypes: ['project.create', 'project.clone', 'project.ready'],
  } }))
  await page.route('**/api/projects/*/observe**', (r) => {
    const u = new URL(r.request().url())
    if (u.pathname.endsWith('/observe/stats')) return r.fulfill({ json: STATS })
    if (u.pathname.endsWith('/observe/errors')) return r.fulfill({ json: ERROR_GROUPS })
    const level = u.searchParams.get('level')
    const source = u.searchParams.get('source')
    const type = u.searchParams.get('type')
    const trace = u.searchParams.get('trace')
    const q = (u.searchParams.get('q') ?? '').toLowerCase()
    const after = Number(u.searchParams.get('after') ?? 0)
    const before = Number(u.searchParams.get('before') ?? 0)
    const limit = Math.min(Number(u.searchParams.get('limit') ?? 200), 1000)
    let out = OBSERVE_LOGS.filter((l) =>
      (!level || level === 'all' || l.level === level) &&
      (!source || source === 'all' || l.source === source) &&
      (!type || l.type === type) &&
      (!trace || l.trace === trace) &&
      (!q || `${l.msg} ${l.type}`.toLowerCase().includes(q)))
    if (before > 0) out = out.filter((l) => l.seq < before).slice(-limit)
    else if (after > 0) out = out.filter((l) => l.seq > after).slice(0, limit)
    else out = out.slice(-limit)
    // Default tail is the last 10 so the Load older button shows.
    if (!u.searchParams.get('after') && !u.searchParams.get('before') && !level && !source && !type && !trace && !q) {
      out = OBSERVE_LOGS.slice(-10)
    }
    return r.fulfill({ json: { logs: out, firstSeq: out[0]?.seq ?? 0, lastSeq: out[out.length - 1]?.seq ?? 0 } })
  })
}

export function terminalUrl(id: string, session: string): string {
  return `/projects/${encodeURIComponent(id)}/terminal/${encodeURIComponent(session)}`
}
