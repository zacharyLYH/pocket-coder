import { useEffect, useRef, type RefObject } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { projectPath } from '@/lib/api'
import { parseTerminalFrame } from '@/lib/terminalFrame'
import { TERM_INPUT_EVENT, TERM_PASTE_EVENT } from '@/components/terminal/QuickKeys'
import { TERM_THEME } from '@/components/terminal/termTheme'

export type ConnStatus = 'connecting' | 'live' | 'ended'

// TerminalPane bridges one xterm.js instance to
// /ws/projects/{id}/sessions/{name}. Frames: input/resize out, output/exit
// in. Re-dials whenever session or redial changes; the host element is
// owned by the parent view.
//
// Scrolling is intentionally NOT intercepted here: xterm.js scrolls its own
// scrollback natively, and under `tmux attach` (alternate screen) the wheel
// reaches tmux via mouse mode (server ThemeArgs sets `mouse on`) so tmux
// scrolls copy-mode instead of emitting Up/Down arrows. A DOM-level wheel
// handler would run after xterm's own handler and double-handle the gesture
// (arrow-key leak = shell history recall, the scroll-up bug).
export function TerminalPane({ projectId, session, redial, fontSize, hostRef, onStatus, onError }: {
  projectId: string
  session: string
  redial: number
  fontSize: number
  hostRef: RefObject<HTMLDivElement | null>
  onStatus: (status: ConnStatus) => void
  onError: (message: string) => void
}) {
  // Live socket for synthetic input (shortcuts modal's key buttons).
  const wsRef = useRef<WebSocket | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)

  // Mobile scroll: xterm.js has no touch handling of its own — the pane is
  // a custom scroll element driven by wheel events only, so a one-finger
  // drag did nothing. A vertical drag is bridged per buffer mode:
  // - normal buffer: xterm's own scrollLines API (negative = earlier
  //   output, positive = newer).
  // - alternate buffer (`tmux attach` in prod): there is no scrollback, so
  //   scrollLines is a noop. Instead emit the same SGR mouse-wheel reports
  //   a trusted wheel would produce (64 = wheel-up, 65 = wheel-down), sent
  //   as input over the session socket so tmux scrolls copy-mode with
  //   `mouse on`. This deliberately does NOT synthesize WheelEvents: xterm
  //   6's smooth-scroll pipeline drops untrusted wheel events, so the old
  //   wheel-bridge never scrolled. Desktop wheel is untouched (no wheel
  //   listener here) — only touch is bridged.
  useEffect(() => {
    if (!hostRef.current) return
    const host: HTMLDivElement = hostRef.current
    const PX_PER_NOTCH = 26
    let active = false
    let startX = 0
    let startY = 0
    let lastY = 0
    let accum = 0
    function onTouchStart(e: TouchEvent) {
      if (e.touches.length !== 1) { active = false; return }
      active = true
      startX = e.touches[0].clientX
      startY = lastY = e.touches[0].clientY
      accum = 0
    }
    function onTouchMove(e: TouchEvent) {
      if (!active || e.touches.length !== 1) return
      const t = e.touches[0]
      // Let horizontal gestures (rare) pass through untouched.
      if (Math.abs(t.clientX - startX) > Math.abs(t.clientY - startY)) return
      // Own the gesture: suppress the browser's own scroll so it can't
      // cancel the drag mid-stream (which is what made the pane unscrollable).
      e.preventDefault()
      accum += t.clientY - lastY
      lastY = t.clientY
      const term = termRef.current
      if (!term) return
      // Alternate screen (`tmux attach` in prod) has no scrollback:
      // scrollLines is a noop there, so forward SGR mouse-wheel reports
      // (64 = wheel-up, 65 = wheel-down) as input — the same bytes a
      // trusted wheel produces, which tmux turns into copy-mode scrolls.
      // Drag down reveals earlier output (wheel-up), push up reveals newer.
      let alternate = false
      try {
        alternate = (term.buffer.active as unknown as { type?: string }).type === 'alternate'
      } catch {
        alternate = false
      }
      if (alternate) {
        const ws = wsRef.current
        if (!ws || ws.readyState !== WebSocket.OPEN) return
        const cols = term.cols || 80
        const rows = term.rows || 24
        const rect = host.getBoundingClientRect()
        const col = rect.width > 0
          ? Math.min(cols, Math.max(1, Math.floor(((t.clientX - rect.left) / rect.width) * cols) + 1))
          : 1
        const row = rect.height > 0
          ? Math.min(rows, Math.max(1, Math.floor(((t.clientY - rect.top) / rect.height) * rows) + 1))
          : 1
        while (Math.abs(accum) >= PX_PER_NOTCH) {
          const step = accum > 0 ? PX_PER_NOTCH : -PX_PER_NOTCH
          accum -= step
          ws.send(JSON.stringify({ type: 'input', data: `\x1b[<${step > 0 ? 64 : 65};${col};${row}M` }))
        }
        return
      }
      // Match finger travel to text lines from live geometry.
      const rows = term.rows || 1
      const pxPerLine = host.clientHeight / rows || 13
      while (Math.abs(accum) >= PX_PER_NOTCH) {
        const step = accum > 0 ? PX_PER_NOTCH : -PX_PER_NOTCH
        accum -= step
        // Native feel: drag down reveals earlier output, push up reveals
        // newer output.
        const lines = Math.max(1, Math.round(PX_PER_NOTCH / pxPerLine))
        term.scrollLines(step > 0 ? -lines : lines)
      }
    }
    function onTouchEnd() { active = false; accum = 0 }
    host.addEventListener('touchstart', onTouchStart, { passive: true })
    host.addEventListener('touchmove', onTouchMove, { passive: false })
    host.addEventListener('touchend', onTouchEnd, { passive: true })
    host.addEventListener('touchcancel', onTouchEnd, { passive: true })
    return () => {
      host.removeEventListener('touchstart', onTouchStart)
      host.removeEventListener('touchmove', onTouchMove)
      host.removeEventListener('touchend', onTouchEnd)
      host.removeEventListener('touchcancel', onTouchEnd)
    }
  }, [hostRef])

  useEffect(() => {
    const onInput = (e: Event) => {
      const ws = wsRef.current
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'input', data: (e as CustomEvent<string>).detail }))
      }
    }
    window.addEventListener(TERM_INPUT_EVENT, onInput)
    const onPaste = (e: Event) => {
      termRef.current?.paste((e as CustomEvent<string>).detail)
    }
    window.addEventListener(TERM_PASTE_EVENT, onPaste)
    return () => {
      window.removeEventListener(TERM_INPUT_EVENT, onInput)
      window.removeEventListener(TERM_PASTE_EVENT, onPaste)
    }
  }, [])

  useEffect(() => {
    let disposed = false
    let opened = false
    let ws: WebSocket | null = null

    const term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'block',
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", "Fira Code", monospace',
      fontSize,
      // Light strokes + 1.3 line height: same content, more air per line.
      fontWeight: '300',
      fontWeightBold: '500',
      lineHeight: 1.3,
      letterSpacing: 0.3,
      theme: TERM_THEME,
      allowProposedApi: true,
      scrollback: 10000,
      scrollSensitivity: 2,
      drawBoldTextInBrightColors: true,
      // Guarantee readable foregrounds on any of the ANSI colours.
      minimumContrastRatio: 4.5,
      // ONE option, two wins: the vertical scrollbar renders 7px instead of
      // the 14px default, AND FitAddon's column budget comes from the same
      // value — so cols track the space actually available instead of
      // reserving a 14px dead gutter (one extra column on a phone).
      overviewRuler: { width: 7 },
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    termRef.current = term
    fitRef.current = fit

    // PTY output is coalesced into one xterm.write() per animation frame.
    // An agent streaming test output sends hundreds of frames a second, and
    // writing each one separately keeps the parser and the DOM renderer on
    // the main thread for the whole burst — typing then lags behind.
    let pending = ''
    let raf = 0
    function flush() {
      raf = 0
      if (!pending || disposed) return
      const data = pending
      pending = ''
      term.write(data)
    }
    function enqueueWrite(data: string) {
      pending += data
      // A backgrounded tab pauses rAF: write through so a busy session
      // cannot grow `pending` without bound.
      if (pending.length > 64_000) { flush(); return }
      if (!raf) raf = requestAnimationFrame(flush)
    }

    const observer = new ResizeObserver(() => {
      if (!opened || !ws) return
      fit.fit()
      sendResize(ws, fit)
    })

    async function ensureSessionThenDial() {
      onStatus('connecting')
      try {
        const res = await fetch(projectPath(projectId, '/sessions'), {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: session }),
        })
        if (disposed) return
        if (!res.ok) {
          const detail = await res.text()
          onStatus('ended')
          onError(`session create failed (${res.status}): ${detail}`)
          return
        }
      } catch {
        if (!disposed) {
          onStatus('ended')
          onError('session create failed — network error')
        }
        return
      }

      const proto = location.protocol === 'https:' ? 'wss' : 'ws'
      ws = new WebSocket(`${proto}://${location.host}/ws/projects/${encodeURIComponent(projectId)}/sessions/${encodeURIComponent(session)}`)
      ws.binaryType = 'arraybuffer'
      wsRef.current = ws

      ws.onopen = () => {
        if (disposed || !ws) return
        opened = true
        term.open(hostRef.current!)
        fit.fit()
        term.focus()
        sendResize(ws, fit)
        onStatus('live')
      }
      ws.onmessage = (ev) => {
        if (disposed) return
        // Malformed or unknown binary frames are ignored so one bad message
        // never kills the terminal.
        const frame = parseTerminalFrame(ev.data as ArrayBuffer | Uint8Array | string)
        if (!frame) return
        if (frame.type === 'output') enqueueWrite(frame.data ?? '')
        if (frame.type === 'exit') {
          onStatus('ended')
          // Drain first so the notice lands after every queued line.
          flush()
          term.write(`\r\n\x1b[31m[session ended${frame.code ? ` (code ${frame.code})` : ''}]\x1b[0m\r\n`)
          ws!.close()
        }
      }
      ws.onclose = () => { if (!disposed) onStatus('ended') }
      ws.onerror = () => { if (!disposed) onStatus('ended') }

      term.onData((data) => {
        if (ws && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'input', data }))
      })
    }
    void ensureSessionThenDial()

    observer.observe(hostRef.current!)
    return () => {
      disposed = true
      cancelAnimationFrame(raf)
      observer.disconnect()
      ws?.close()
      if (wsRef.current === ws) wsRef.current = null
      termRef.current = null
      fitRef.current = null
      term.dispose()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, session, redial])

  // Zoom: apply without redialing — setOption reflows, fit recomputes
  // dimensions, and the new size goes to tmux like any resize.
  useEffect(() => {
    const term = termRef.current
    const fit = fitRef.current
    if (!term || !fit) return
    term.options.fontSize = fontSize
    fit.fit()
    const ws = wsRef.current
    if (ws) sendResize(ws, fit)
  }, [fontSize])

  return null
}

function sendResize(ws: WebSocket, fit: FitAddon) {
  if (ws.readyState !== WebSocket.OPEN) return
  const dims = fit.proposeDimensions()
  if (dims && dims.rows > 0 && dims.cols > 0) {
    ws.send(JSON.stringify({ type: 'resize', rows: dims.rows, cols: dims.cols }))
  }
}
