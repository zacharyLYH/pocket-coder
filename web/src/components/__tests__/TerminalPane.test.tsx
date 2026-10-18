import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import type { RefObject } from 'react'
import { render } from '@testing-library/react'
import { TerminalPane } from '@/components/terminal/TerminalPane'
import { mockFetch } from '@/test/mockFetch'
import { MockWebSocket, stubWebSocket } from '@/test/stubs'

function hostRef(): RefObject<HTMLDivElement | null> {
  return { current: document.createElement('div') }
}

function outputFrame(text: string): Uint8Array {
  const payload = new TextEncoder().encode(text)
  const frame = new Uint8Array(payload.length + 1)
  frame[0] = 0
  frame.set(payload, 1)
  return frame
}

function exitFrame(code: number): Uint8Array {
  const frame = new Uint8Array(5)
  frame[0] = 1
  new DataView(frame.buffer).setInt32(1, code, false)
  return frame
}

let wsInstances: MockWebSocket[] = []

// Buffer mode drives the touch bridge: 'normal' has scrollback,
// 'alternate' is `tmux attach` (no scrollback, tmux owns scroll via mouse).
const { scrollLines, opened, opts, buf, write } = vi.hoisted(() => ({
  scrollLines: vi.fn(),
  opened: vi.fn(),
  opts: { v: null as Record<string, unknown> | null },
  buf: { type: 'normal' as 'normal' | 'alternate' },
  write: vi.fn(),
}))

vi.mock('@xterm/xterm', () => ({
  Terminal: function (options?: Record<string, unknown>) {
    opts.v = options ?? null
    const term = {
      open: opened,
      dispose: vi.fn(),
      focus: vi.fn(),
      write,
      onData: vi.fn(),
      scrollLines,
      cols: 80,
      rows: 24,
      buffer: { active: { baseY: 0, length: 100, type: 'normal' as string } },
      options: {},
      loadAddon: vi.fn(),
    }
    // Read the live mode at access time so each test controls it.
    Object.defineProperty(term.buffer.active, 'type', { get: () => buf.type, configurable: true })
    return term
  },
}))

vi.mock('@xterm/addon-fit', () => ({
  FitAddon: function () {
    return { fit: vi.fn(), proposeDimensions: vi.fn(() => ({ rows: 24, cols: 80 })) }
  },
}))

beforeEach(() => {
  wsInstances = stubWebSocket()
  scrollLines.mockClear()
  opened.mockClear()
  write.mockClear()
  opts.v = null
  buf.type = 'normal'
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} } as unknown as typeof ResizeObserver)
  vi.stubGlobal('fetch', mockFetch((url) => {
    if (url.endsWith('/sessions')) return { status: 200, body: { sessions: [{ name: 'main' }] } }
    return undefined
  }))
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('TerminalPane liveness + spaciousness options', () => {
  // The palette/typography/fit config is the whole "lively, not white on
  // black" + "spacious, not cramped" change: pin it so a future option edit
  // cannot silently drop it.
  it('opens with the vibrant theme, airy weights and a 7px scrollbar budget', () => {
    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={hostRef()} onStatus={vi.fn()} onError={vi.fn()} />)
    expect(opts.v).toEqual(expect.objectContaining({
      fontWeight: '300',
      fontWeightBold: '500',
      lineHeight: 1.3,
      drawBoldTextInBrightColors: true,
      minimumContrastRatio: 4.5,
      // 7px is both the rendered scrollbar width and FitAddon's column
      // budget, so no 14px dead gutter is reserved.
      overviewRuler: { width: 7 },
      theme: expect.objectContaining({
        background: '#1a1b26',
        foreground: '#c0caf5',
        green: '#9ece6a',
        scrollbarSliderBackground: '#56576a',
      }),
    }))
  })
})

describe('TerminalPane output coalescing', () => {
  // One write per animation frame: a burst of PTY frames used to hit
  // term.write() once per frame, which kept the parser and DOM renderer on
  // the main thread for the whole burst.
  it('coalesces a burst of output frames into a single write', async () => {
    const scheduled: FrameRequestCallback[] = []
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => { scheduled.push(cb); return scheduled.length })
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={hostRef()} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))
    expect(wsInstances[0]?.binaryType).toBe('arraybuffer')

    const send = (data: string) => wsInstances[0]!.onmessage!({ data: outputFrame(data) } as MessageEvent)
    for (let i = 0; i < 50; i++) send(`line-${i}\r\n`)

    expect(scheduled.length).toBe(1) // one frame scheduled for 50 frames of output
    expect(write).not.toHaveBeenCalled()
    scheduled.shift()!(0)
    expect(write).toHaveBeenCalledTimes(1)
    expect(write.mock.calls[0][0]).toContain('line-0')
    expect(write.mock.calls[0][0]).toContain('line-49')
    expect(scheduled.length).toBe(0) // nothing left over
  })

  it('drains queued output before the session-ended notice', async () => {
    // rAF never fires here: the exit frame must flush the queue itself, or
    // the last lines of a finished session are lost.
    vi.stubGlobal('requestAnimationFrame', vi.fn(() => 1))
    vi.stubGlobal('cancelAnimationFrame', vi.fn())

    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={hostRef()} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))

    const send = (data: Uint8Array) => wsInstances[0]!.onmessage!({ data } as MessageEvent)
    send(outputFrame('tail-line\r\n'))
    send(exitFrame(1))

    const written = write.mock.calls.map(([d]) => d as string).join('')
    expect(written).toContain('tail-line')
    expect(written.indexOf('tail-line')).toBeLessThan(written.indexOf('session ended'))
  })
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
  // xterm has no touch scrolling of its own, so a one-finger drag is bridged
  // per buffer mode (synthetic WheelEvents are swallowed by xterm 6's
  // smooth-scroll pipeline, so a wheel-bridge never scrolled). The handler
  // owns the gesture (preventDefault) so the browser cannot cancel it.
  it('bridges a vertical touch drag into scrollLines on the normal buffer (negative = earlier output)', async () => {
    buf.type = 'normal'
    const host = hostRef()
    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={host} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))

    const fire = (type: 'touchstart' | 'touchmove' | 'touchend', y: number) => {
      const ev = new Event(type, { bubbles: true, cancelable: true }) as Event & {
        touches: Array<{ clientX: number; clientY: number }>
        changedTouches: Array<{ clientX: number; clientY: number }>
      }
      const touch = { clientX: 10, clientY: y }
      ev.touches = type === 'touchend' ? [] : [touch]
      ev.changedTouches = [touch]
      host.current!.dispatchEvent(ev)
      return ev
    }

    fire('touchstart', 100)
    const move = fire('touchmove', 170) // 70px drag down
    fire('touchend', 170)

    expect(move.defaultPrevented).toBe(true)
    expect(scrollLines).toHaveBeenCalled()
    // Native feel: drag down reveals earlier output (negative), push up
    // reveals newer (positive). Never Up/Down input escapes.
    expect(scrollLines.mock.calls.every(([n]) => (n as number) < 0)).toBe(true)
    expect(wsInstances[0]?.sent.filter((s) => s.includes('"input"'))).toEqual([])
  })

  // Prod is `tmux attach` (alternate screen): no scrollback, so scrollLines
  // is a noop. The bridge must send SGR mouse-wheel reports (64 = wheel-up,
  // 65 = wheel-down) as input so tmux scrolls copy-mode — the same bytes a
  // trusted desktop wheel produces. This is the PWA finger-swipe regression
  // pin: the old scrollLines-only bridge passed unit tests yet did nothing
  // on real phones.
  it('sends SGR mouse-wheel input (not scrollLines) on the alternate buffer', async () => {
    buf.type = 'alternate'
    const host = hostRef()
    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={host} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))

    const fire = (type: 'touchstart' | 'touchmove' | 'touchend', y: number) => {
      const ev = new Event(type, { bubbles: true, cancelable: true }) as Event & {
        touches: Array<{ clientX: number; clientY: number }>
        changedTouches: Array<{ clientX: number; clientY: number }>
      }
      const touch = { clientX: 10, clientY: y }
      ev.touches = type === 'touchend' ? [] : [touch]
      ev.changedTouches = [touch]
      host.current!.dispatchEvent(ev)
      return ev
    }

    fire('touchstart', 100)
    const move = fire('touchmove', 170) // 70px drag down → wheel-up (64)
    fire('touchend', 170)

    expect(move.defaultPrevented).toBe(true)
    expect(scrollLines).not.toHaveBeenCalled()
    const inputs = wsInstances[0]?.sent.filter((s) => s.includes('"input"')) ?? []
    expect(inputs.length).toBeGreaterThan(0)
    // Drag down reveals earlier output: wheel-up reports only.
    expect(inputs.every((s) => s.includes('\\u001b[<64;'))).toBe(true)
    // Never arrow-key leaks (the original desktop scroll bug).
    expect(inputs.filter((s) => s.includes('\\u001b[A') || s.includes('\\u001b[B'))).toEqual([])
  })

  it('sends wheel-down reports when pushing up on the alternate buffer', async () => {
    buf.type = 'alternate'
    const host = hostRef()
    render(<TerminalPane projectId="abc" session="main" redial={0} fontSize={14} hostRef={host} onStatus={vi.fn()} onError={vi.fn()} />)
    await new Promise((r) => setTimeout(r, 50))
    wsInstances[0]?.open()
    await new Promise((r) => setTimeout(r, 10))

    const fire = (type: 'touchstart' | 'touchmove' | 'touchend', y: number) => {
      const ev = new Event(type, { bubbles: true, cancelable: true }) as Event & {
        touches: Array<{ clientX: number; clientY: number }>
        changedTouches: Array<{ clientX: number; clientY: number }>
      }
      const touch = { clientX: 10, clientY: y }
      ev.touches = type === 'touchend' ? [] : [touch]
      ev.changedTouches = [touch]
      host.current!.dispatchEvent(ev)
      return ev
    }

    fire('touchstart', 170)
    fire('touchmove', 100) // 70px push up → wheel-down (65)
    fire('touchend', 100)

    expect(scrollLines).not.toHaveBeenCalled()
    const inputs = wsInstances[0]?.sent.filter((s) => s.includes('"input"')) ?? []
    expect(inputs.length).toBeGreaterThan(0)
    expect(inputs.every((s) => s.includes('\\u001b[<65;'))).toBe(true)
  })

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
