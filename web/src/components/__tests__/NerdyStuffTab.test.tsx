import { render, screen, waitFor, fireEvent, act } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'

import { NerdyStuffTab } from '@/components/terminal/NerdyStuffTab'

const LOGS = [
  { seq: 1, ts: new Date().toISOString(), project: 'abc', trace: 'aaaaaaaaaaaaaaaa', source: 'server', level: 'info', type: 'project.create', msg: 'project created' },
  { seq: 2, ts: new Date().toISOString(), project: 'abc', trace: 'bbbbbbbbbbbbbbbb', source: 'server', level: 'error', type: 'clone.failed', msg: 'clone failed sha abc1234' },
]

const STATS = {
  ts: new Date().toISOString(), state: 'running', cpuPercent: 12.5,
  memUsed: 512 * 1024 ** 2, memLimit: 16 * 1024 ** 3,
  netRx: 100, netTx: 200, blockR: 1024, blockW: 2048, pids: 7,
  diskUsed: 200 * 1024, diskTotal: 1000 * 1024,
}

function stubFetch(calls: string[], opts: {
  observe?: (url: string) => unknown
  errors?: unknown
  health?: unknown
} | ((url: string) => unknown) = {}) {
  const { observe: observeImpl, errors = { groups: [] }, health } =
    typeof opts === 'function' ? { observe: opts, errors: undefined, health: undefined } : opts
  const fetchMock = vi.fn(async (url: string) => {
    calls.push(String(url))
    const u = String(url)
    if (u.includes('/observe/errors')) return new Response(JSON.stringify(errors), { status: 200 })
    if (u.includes('/observe/stats')) return new Response(JSON.stringify(STATS), { status: 200 })
    if (u.includes('/observe/meta')) return new Response(JSON.stringify({
      auditTypes: ['project.create', 'terminal.attach', 'session.create'],
      buildTypes: ['project.create'],
    }), { status: 200 })
    if (u.endsWith('/health') && health !== undefined) return new Response(JSON.stringify(health), { status: 200 })
    if (u.includes('/observe')) {
      if (observeImpl) return new Response(JSON.stringify(observeImpl(u)), { status: 200 })
      return new Response(JSON.stringify({ logs: LOGS, firstSeq: 1, lastSeq: 2 }), { status: 200 })
    }
    return new Response(JSON.stringify({}), { status: 200 })
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('NerdyStuffTab', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('lands on overview first', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await waitFor(() => expect(screen.getByTestId('nerdy-overview')).toBeVisible())
    expect(screen.queryByTestId('nerdy-list')).toBeNull()
  })

  async function gotoRuntime() {
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-runtime')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-list')).toBeVisible())
  }

  it('tails the observe log', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    expect(screen.getByTestId('nerdy-list')).toHaveTextContent('project created')
    expect(screen.getByTestId('nerdy-row-2')).toHaveTextContent('clone failed')
  })

  it('shows the nerdy empty state', async () => {
    const calls: string[] = []
    stubFetch(calls, () => ({ logs: [], firstSeq: 0, lastSeq: 0 }))
    render(<NerdyStuffTab projectId="abc" />)
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-runtime')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-empty')).toBeVisible())
    expect(screen.getByTestId('nerdy-empty')).toHaveTextContent('Nothing nerdy yet')
  })

  it('chip filter narrows the tail', async () => {
    const calls: string[] = []
    stubFetch(calls, (u) => {
      const level = new URL(u, 'http://x').searchParams.get('level')
      const logs = level === 'error' ? LOGS.filter((l) => l.level === 'error') : LOGS
      return { logs, firstSeq: 1, lastSeq: 2 }
    })
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-level-error')) })
    await waitFor(() => expect(calls.some((c) => c.includes('level=error'))).toBe(true))
  })

  it('expand shows JSON type and trace', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-row-toggle-2')) })
    expect(screen.getByTestId('nerdy-expand-2')).toHaveTextContent('clone.failed')
    expect(screen.getByTestId('nerdy-expand-2')).toHaveTextContent('bbbbbbbbbbbbbbbb')
  })

  it('load older pages with the before cursor', async () => {
    const calls: string[] = []
    stubFetch(calls, {
      observe: (u) => {
        // firstSeq=5 shows the button; the before page returns the older row
        if (u.includes('before=')) return { logs: [LOGS[0]], firstSeq: 1, lastSeq: 1 }
        return { logs: LOGS, firstSeq: 5, lastSeq: 6 }
      },
    })
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    await waitFor(() => expect(screen.getByTestId('nerdy-load-older')).toBeVisible())
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-load-older')) })
    await waitFor(() => expect(calls.some((c) => c.includes('before=5'))).toBe(true))
  })

  it('overview shows cpu, meters, and io', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-overview')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-cpu')).toBeVisible())
    expect(screen.getByTestId('nerdy-cpu')).toHaveTextContent('12.5%')
    expect(screen.getByTestId('nerdy-mem')).toHaveTextContent('512.0 MB / 16.0 GB')
    expect(screen.getByTestId('nerdy-disk')).toHaveTextContent('200.0 KB / 1000.0 KB')
    expect(screen.getByTestId('nerdy-pids')).toHaveTextContent('7 pids')
    expect(screen.getByTestId('nerdy-io')).toHaveTextContent('net ↓ 100 B')
    // activity and sessions panels are gone
    expect(screen.queryByTestId('nerdy-panel-activity')).toBeNull()
    expect(screen.queryByTestId('nerdy-panel-sessions')).toBeNull()
  })

  it('highlights toggle keeps only moments that changed something', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    expect(screen.getByTestId('nerdy-highlights')).toHaveTextContent('Highlights')
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-highlights')) })
    await waitFor(() => expect(calls.some((c) => c.includes('type=project.create'))).toBe(true))
    await waitFor(() => expect(calls.some((c) => decodeURIComponent(c).includes(',terminal.attach,'))).toBe(true))
  })

  it('panel dropdown and filters toggle work', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    // dropdown switches panels (Radix menus open on pointerdown in jsdom)
    await act(async () => {
      fireEvent.pointerDown(screen.getByTestId('nerdy-panel-select'))
      fireEvent.click(screen.getByTestId('nerdy-panel-select'))
    })
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-opt-overview')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-overview')).toBeVisible())
    await act(async () => {
      fireEvent.pointerDown(screen.getByTestId('nerdy-panel-select'))
      fireEvent.click(screen.getByTestId('nerdy-panel-select'))
    })
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-opt-runtime')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-list')).toBeVisible())
    // filters toggle carries the active count and still filters
    expect(screen.getByTestId('nerdy-filters-toggle')).toHaveTextContent('Filters')
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-level-error')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-filters-toggle')).toHaveTextContent('Filters (1)'))
    await waitFor(() => expect(calls.some((c) => c.includes('level=error'))).toBe(true))
  })

  it('follow pauses and resumes', async () => {
    const calls: string[] = []
    stubFetch(calls)
    render(<NerdyStuffTab projectId="abc" />)
    await gotoRuntime()
    await waitFor(() => expect(screen.getByTestId('nerdy-follow')).toBeVisible())
    expect(screen.getByTestId('nerdy-follow')).toHaveTextContent('Following')
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-follow')) })
    expect(screen.getByTestId('nerdy-follow')).toHaveTextContent('Follow')
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-follow')) })
    expect(screen.getByTestId('nerdy-follow')).toHaveTextContent('Following')
  })

  it('error group jumps to its trace and the chip clears it', async () => {
    const calls: string[] = []
    stubFetch(calls, {
      errors: {
        groups: [{
          key: 'k', type: 'clone.failed', count: 2, firstSeen: new Date().toISOString(),
          lastSeen: new Date().toISOString(), sampleTrace: 'bbbbbbbbbbbbbbbb', sampleMsg: 'clone failed',
        }],
      },
    })
    render(<NerdyStuffTab projectId="abc" />)
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-panel-errors')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-error-clone.failed')).toBeVisible())
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-error-clone.failed')) })
    await waitFor(() => expect(calls.some((c) => c.includes('trace=bbbbbbbbbbbbbbbb'))).toBe(true))
    // The active trace is visible, counts as a filter, and clears on click.
    await waitFor(() => expect(screen.getByTestId('nerdy-trace-chip')).toBeVisible())
    expect(screen.getByTestId('nerdy-filters-toggle')).toHaveTextContent('Filters (1)')
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-trace-chip')) })
    await waitFor(() => expect(screen.queryByTestId('nerdy-trace-chip')).toBeNull())
  })

  it('healthcheck runs once per click and prints every divergence both ways', async () => {
    const calls: string[] = []
    const fetchMock = stubFetch(calls, {
      health: {
        project: 'abc',
        inSync: false,
        checks: [
          { name: 'container', status: 'ok', state: 'recorded', system: 'running' },
          { name: 'session:ghost-1', status: 'drift', state: 'recorded harness fake', system: 'missing from tmux', detail: 'recorded but not running' },
          { name: 'harness:fake', status: 'drift', state: 'installed', system: 'missing', detail: 'recorded installed but the binary is missing' },
          { name: 'harness:helper', status: 'drift', state: 'not recorded', system: 'binary present', detail: 'binary present but not recorded' },
        ],
      },
    })
    render(<NerdyStuffTab projectId="abc" />)
    await waitFor(() => expect(screen.getByTestId('nerdy-healthcheck')).toBeVisible())

    // One click → exactly one synchronous run.
    await act(async () => { fireEvent.click(screen.getByTestId('nerdy-healthcheck-run')) })
    await waitFor(() => expect(screen.getByTestId('nerdy-healthcheck-report')).toBeVisible())
    const healthCalls = fetchMock.mock.calls.filter(([u]) => String(u).endsWith('/health'))
    expect(healthCalls).toHaveLength(1)
    const [, healthInit] = healthCalls[0] as unknown as [string, RequestInit?]
    expect(healthInit?.method).toBe('POST')

    // Verdict counts the drifts; both directions are named explicitly.
    expect(screen.getByTestId('nerdy-healthcheck-verdict')).toHaveTextContent('3 drifts found')
    expect(screen.getByTestId('nerdy-healthcheck-row-session:ghost-1')).toHaveTextContent('missing from tmux')
    expect(screen.getByTestId('nerdy-healthcheck-row-harness:fake')).toHaveTextContent('recorded installed but the binary is missing')
    expect(screen.getByTestId('nerdy-healthcheck-row-harness:helper')).toHaveTextContent('binary present but not recorded')
  })
})
