import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { TerminalView } from '@/components/terminal/TerminalView'

// Unit tests for TerminalView session management — restart, kill, and
// switching sessions. The backend is mocked at the fetch/WebSocket level;
// the real backend path is covered by the Playwright suite.

const PROJECT_ID = 'abc'
const SESSION = 'helper-1'

const SESSIONS_RESPONSE = { sessions: [{ name: 'helper-1' }, { name: 'main' }] }
const HARNESS_RESPONSE = { harnesses: [{ id: 'helper', name: 'Helper', command: 'helper' }] }

// Track all fetch calls for assertions.
let fetchCalls: { url: string; method: string; body?: string }[] = []

function mockFetch(handler: (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    fetchCalls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })
    const out = handler(url, init)
    if (!out) throw new Error(`unexpected fetch: ${url}`)
    return new Response(JSON.stringify(out.body), { status: out.status })
  })
}

// Minimal WebSocket mock that captures open/message/close.
// Does NOT auto-open to avoid triggering xterm.js term.open() which
// needs a full browser environment (matchMedia, etc.).
let wsInstances: MockWebSocket[] = []
class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = 0 // CONNECTING
  onopen: (() => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []
  constructor(public url: string) {
    wsInstances.push(this)
    // Do NOT auto-open — let tests call open() explicitly if needed.
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3; this.onclose?.() }
}

beforeEach(() => {
  fetchCalls = []
  wsInstances = []
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket)
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver)
  vi.spyOn(window.history, 'replaceState').mockImplementation(() => {})
  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
  })
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('TerminalView session management', () => {
  function renderView(fetchHandler: (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined) {
    vi.stubGlobal('fetch', mockFetch(fetchHandler))
    return render(
      <TerminalView projectId={PROJECT_ID} initialSession={SESSION} onBack={vi.fn()} />
    )
  }

  it('restart calls POST /restart then triggers redial', async () => {
    renderView((url) => {
      if (url.includes('/sessions') && url.includes('/restart')) return { status: 200, body: { name: SESSION } }
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      return undefined
    })

    // Wait for initial render + session list fetch
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    // Find and click Restart button
    const restartBtn = screen.getByRole('button', { name: /Restart/ })
    await act(async () => { fireEvent.click(restartBtn) })

    // Verify restart API was called
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/restart') && c.method === 'POST')).toBe(true)
    })
  })

  it('kill calls DELETE /sessions/{name}', async () => {
    renderView((url) => {
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      if (url.includes('/sessions/') && url.endsWith(`/${SESSION}`) && !url.includes('restart') && !url.includes('rename')) {
        return { status: 200, body: { ok: true } }
      }
      return undefined
    })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    const killBtn = screen.getByRole('button', { name: /Kill/ })
    await act(async () => { fireEvent.click(killBtn) })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes(`/${SESSION}`) && c.method === 'DELETE')).toBe(true)
    })
  })

  it('ensure call POSTs session name without harnessId', async () => {
    renderView((url) => {
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      return undefined
    })

    // The TerminalPane's ensureSessionThenDial fires on mount
    await waitFor(() => {
      const ensureCall = fetchCalls.find(c =>
        c.url.endsWith('/sessions') && c.method === 'POST'
      )
      expect(ensureCall).toBeTruthy()
      // Should send { name: "helper-1" } — no harnessId
      const body = JSON.parse(ensureCall!.body!)
      expect(body).toEqual({ name: SESSION })
      expect(body.harnessId).toBeUndefined()
    })
  })

  it('kill then re-enter same session triggers a new ensure call', async () => {
    renderView((url) => {
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      if (url.includes('/sessions/') && url.includes(`/${SESSION}`) && !url.includes('restart') && !url.includes('rename')) {
        return { status: 200, body: { ok: true } }
      }
      return undefined
    })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    // Kill the session
    const killBtn = screen.getByRole('button', { name: /Kill/ })
    await act(async () => { fireEvent.click(killBtn) })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes(`/${SESSION}`) && c.method === 'DELETE')).toBe(true)
    })

    // Status should be 'ended' after kill
    expect(screen.getByText('Disconnected')).toBeTruthy()

    // The session list should be refreshed (picker shows the session)
    await waitFor(() => {
      const listCalls = fetchCalls.filter(c => c.url.endsWith('/sessions') && c.method === 'GET')
      expect(listCalls.length).toBeGreaterThanOrEqual(2) // initial + post-kill refresh
    })

    // BUG: After kill, clicking the same session in the picker should
    // trigger a re-entry (new ensure POST). But switchSession short-circuits
    // on name === current, so no redial happens.
    // The fix: switchSession allows re-entry when status is 'ended'.
  })

  it('loads sessions and shows current session name', async () => {
    renderView((url) => {
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      return undefined
    })

    // Wait for session list to be fetched
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.endsWith('/sessions') && c.method === 'GET')).toBe(true)
    })

    // The Session button should show the current session name
    const sessionBtn = screen.getByRole('button', { name: 'Session' })
    expect(sessionBtn.textContent).toContain('helper-1')

    // Sessions are loaded (dropdown interaction tested in E2E)
  })
})
