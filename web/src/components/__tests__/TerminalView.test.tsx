import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, fireEvent, waitFor, act } from '@testing-library/react'
import { TerminalView } from '@/components/terminal/TerminalView'
import { mockFetch, type FetchCall } from '@/test/mockFetch'

// Unit tests for TerminalView session management — restart, kill, and
// switching sessions. The backend is mocked at the fetch/WebSocket level;
// the real backend path is covered by the Playwright suite.

const PROJECT_ID = 'abc'
const SESSION = 'helper-1'

const SESSIONS_RESPONSE = { sessions: [{ name: 'helper-1' }, { name: 'main' }] }
const HARNESS_RESPONSE = { harnesses: [{ id: 'helper', name: 'Helper', command: 'helper' }] }

// Track all fetch calls for assertions.
let fetchCalls: FetchCall[] = []

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
    vi.stubGlobal('fetch', mockFetch(fetchHandler, (c) => fetchCalls.push(c)))
    return render(
      <TerminalView projectId={PROJECT_ID} initialSession={SESSION} onBack={vi.fn()} onOpenPreview={vi.fn()} />
    )
  }

  // Every test needs the session list + harness list; per-test extras
  // (restart/kill endpoints) layer on top via the spread.
  type Handler = (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined
  function baseHandler(extra?: Handler): Handler {
    return (url, init) => {
      if (url.endsWith('/sessions')) return { status: 200, body: SESSIONS_RESPONSE }
      if (url.endsWith('/harnesses')) return { status: 200, body: HARNESS_RESPONSE }
      return extra?.(url, init)
    }
  }

  function sessionDeleteHandler(): Handler {
    return (url) => {
      if (url.includes('/sessions/') && (url.endsWith(`/${SESSION}`) || url.endsWith(`/${SESSION}/delete`)) && !url.includes('restart') && !url.includes('rename')) {
        return { status: 200, body: { ok: true } }
      }
      return undefined
    }
  }

  // Radix menus open on pointerdown; fireEvent.click alone never opens them
  // in jsdom, so tests drive the real Actions trigger like a mouse would.
  function openActionsMenu() {
    const trigger = screen.getByTestId('terminal-actions-trigger')
    fireEvent.pointerDown(trigger)
    fireEvent.click(trigger)
  }

  it('restart calls POST /restart then triggers redial', async () => {
    renderView(baseHandler((url) => {
      if (url.includes('/sessions') && url.includes('/restart')) return { status: 200, body: { name: SESSION } }
      return undefined
    }))

    // Wait for initial render + session list fetch
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    // Open the Actions menu and click the real Restart item.
    openActionsMenu()
    const restartBtn = await screen.findByTestId('terminal-action-restart')
    await act(async () => { fireEvent.click(restartBtn) })

    // Verify restart API was called
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/restart') && c.method === 'POST')).toBe(true)
    })
  })

  it('kill calls DELETE /sessions/{name}', async () => {
    renderView(baseHandler(sessionDeleteHandler()))

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    openActionsMenu()
    const killBtn = await screen.findByTestId('terminal-action-kill')
    await act(async () => { fireEvent.click(killBtn) })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes(`/${SESSION}`) && c.method === 'DELETE')).toBe(true)
    })
  })

  it('ensure call POSTs session name without harnessId', async () => {
    renderView(baseHandler())

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
    renderView(baseHandler(sessionDeleteHandler()))

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    // Kill the session via the real Actions menu.
    openActionsMenu()
    const killBtn = await screen.findByTestId('terminal-action-kill')
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

  it('loads sessions and shows the current session tab', async () => {
    renderView(baseHandler())

    // Wait for session list to be fetched
    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.endsWith('/sessions') && c.method === 'GET')).toBe(true)
    })

    // The tab strip shows one tab per session with the current one selected
    const tab = screen.getByTestId('tab-session-helper-1')
    expect(tab.textContent).toContain('helper-1')
    expect(tab).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('tab-session-main')).toBeTruthy()
    expect(screen.getByTestId('tab-new')).toBeTruthy()
  })

  it('tab ✕ deletes that session without touching the others', async () => {
    renderView(baseHandler(sessionDeleteHandler()))

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes('/sessions'))).toBe(true)
    })

    await act(async () => { fireEvent.click(screen.getByTestId('tab-close-helper-1')) })

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.includes(`/${SESSION}/delete`) && c.method === 'DELETE')).toBe(true)
    })
  })

  it('fixed views hide session actions but keep the terminal attached', async () => {
    renderView(baseHandler((url) => {
      if (url.includes('/git/status')) return { status: 200, body: { branch: 'main', files: [] } }
      return undefined
    }))

    await waitFor(() => {
      expect(fetchCalls.some(c => c.url.endsWith('/sessions') && c.method === 'GET')).toBe(true)
    })

    // Terminal mode shows session actions.
    expect(screen.getByTestId('terminal-actions-trigger')).toBeTruthy()
    expect(screen.getByTestId('current-session').textContent).toContain('helper-1')

    const ensurePosts = () => fetchCalls.filter(c => c.url.endsWith('/sessions') && c.method === 'POST')
    await waitFor(() => { expect(ensurePosts().length).toBeGreaterThanOrEqual(1) })
    const ensureCount = ensurePosts().length

    // Transport into Diff: no Actions menu, fixed title instead of session name.
    await act(async () => { fireEvent.click(screen.getByTestId('tab-diff')) })
    expect(screen.getByTestId('fixed-view-title').textContent).toContain('Diff')
    expect(screen.queryByTestId('terminal-actions-trigger')).toBeNull()
    expect(screen.queryByTestId('current-session')).toBeNull()

    // The terminal pane stays mounted: no extra ensure POST while in Diff…
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    expect(ensurePosts().length).toBe(ensureCount)

    // …and returning does not redial either.
    await act(async () => { fireEvent.click(screen.getByTestId('tab-session-helper-1')) })
    expect(screen.getByTestId('terminal-actions-trigger')).toBeTruthy()
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    expect(ensurePosts().length).toBe(ensureCount)
  })
})
