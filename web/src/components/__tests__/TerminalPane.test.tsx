import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { RefObject } from 'react'
import { render } from '@testing-library/react'
import { TerminalPane } from '@/components/terminal/TerminalPane'
import { mockFetch } from '@/test/mockFetch'

function hostRef(): RefObject<HTMLDivElement | null> {
  return { current: document.createElement('div') }
}

let wsInstances: MockWebSocket[] = []
class MockWebSocket {
  static CONNECTING = 0
  static OPEN = 1
  static CLOSING = 2
  static CLOSED = 3
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((ev: MessageEvent) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  sent: string[] = []
  constructor(public url: string) {
    wsInstances.push(this)
  }
  open() {
    this.readyState = 1
    this.onopen?.()
  }
  send(data: string) { this.sent.push(data) }
  close() { this.readyState = 3; this.onclose?.() }
}

const { scrollLines, opened, opts } = vi.hoisted(() => ({
  scrollLines: vi.fn(),
  opened: vi.fn(),
  opts: { v: null as Record<string, unknown> | null },
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: function (options?: Record<string, unknown>) {
    opts.v = options ?? null
    return {
      open: opened,
      dispose: vi.fn(),
      focus: vi.fn(),
      write: vi.fn(),
      onData: vi.fn(),
      scrollLines,
      buffer: { active: { baseY: 0, length: 100 } },
      options: {},
      loadAddon: vi.fn(),
    }
  },
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: function () {
    return { fit: vi.fn(), proposeDimensions: vi.fn(() => ({ rows: 24, cols: 80 })) }
  },
}))

beforeEach(() => {
  wsInstances = []
  scrollLines.mockClear()
  opened.mockClear()
  opts.v = null
  vi.stubGlobal('WebSocket', MockWebSocket as unknown as typeof WebSocket)
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver)
  vi.stubGlobal('fetch', mockFetch((url) => {
    if (url.endsWith('/sessions')) return { status: 200, body: { sessions: [{ name: 'main' }] } }
    return undefined
  }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('TerminalPane scroll behavior', () => {
  it('creates Terminal with scrollback and scrollSensitivity', () => {
    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={hostRef()} onStatus={vi.fn()} onError={vi.fn()} />)
    expect(opts.v).toEqual(expect.objectContaining({ scrollback: 10000, scrollSensitivity: 2 }))
  })

  // Scrolling must reach xterm natively: no DOM-level wheel hijack that runs
  // after xterm's own handler (that double-handled the gesture and leaked
  // Up/Down arrows into the shell). A wheel over the host sends nothing and
  // scrolls nothing by itself — xterm + tmux mouse mode own the gesture.
  it('does not hijack wheel events into scrollLines/input', async () => {
    const host = hostRef()

    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={host} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))

    expect(opened).toHaveBeenCalled()

    // Dispatch on the element itself: no document attachment needed, the
    // pane's listeners (if any) sit on the host.
    const wheelEvent = new WheelEvent('wheel', { deltaY: 40, deltaMode: 0, cancelable: true })
    host.current!.dispatchEvent(wheelEvent)

    expect(scrollLines).not.toHaveBeenCalled()
    expect(wsInstances[0]?.sent.filter((s) => s.includes('"input"'))).toEqual([])
    expect(wheelEvent.defaultPrevented).toBe(false)
  })
})
