import { useEffect, useRef, type RefObject } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { projectPath } from '@/lib/api'
import { parseTerminalFrame } from '@/lib/terminalFrame'
import { TERM_INPUT_EVENT } from '@/components/terminal/QuickKeys'

export type ConnStatus = 'connecting' | 'live' | 'ended'

// A dark theme that feels like a real terminal, not a default palette.
// Kept in sync with the tmux status styling the server applies on session
// create (ThemeArgs in server/internal/session/session.go).
const TERM_THEME = {
  background: '#0a0e14',
  foreground: '#b3b1ad',
  cursor: '#e6b450',
  cursorAccent: '#0a0e14',
  selectionBackground: '#273747',
  selectionForeground: '#e6e4d9',
  black: '#01060e',
  red: '#ea6c73',
  green: '#91b362',
  yellow: '#f9af4f',
  blue: '#53bdfa',
  magenta: '#fae994',
  cyan: '#90e1c6',
  white: '#c7c7c7',
  brightBlack: '#686868',
  brightRed: '#f07178',
  brightGreen: '#c2d94c',
  brightYellow: '#ffb454',
  brightBlue: '#59c2ff',
  brightMagenta: '#ffee99',
  brightCyan: '#95e6cb',
  brightWhite: '#ffffff',
}

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
  // drag did nothing. Translate a vertical drag into synthetic wheel events
  // dispatched at .xterm-screen (the scroll element's node): xterm then
  // handles the gesture exactly like a real wheel, including forwarding it
  // to tmux as a mouse-wheel when mouse mode is active. Desktop wheel is
  // untouched (no wheel listener here) — only touch is bridged.
  useEffect(() => {
    const host = hostRef.current
    if (!host) return
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
      const screen = host?.querySelector('.xterm-screen')
      if (!screen) return
      while (Math.abs(accum) >= PX_PER_NOTCH) {
        const step = accum > 0 ? PX_PER_NOTCH : -PX_PER_NOTCH
        accum -= step
        // Drag down reveals earlier output: positive deltaY scrolls xterm up.
        screen.dispatchEvent(new WheelEvent('wheel', {
          deltaY: step, deltaMode: 0, clientX: t.clientX, clientY: t.clientY,
          bubbles: true, cancelable: true,
        }))
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
    return () => window.removeEventListener(TERM_INPUT_EVENT, onInput)
  }, [])

  useEffect(() => {
    let disposed = false
    let opened = false
    let ws: WebSocket | null = null

    const term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", "Fira Code", monospace',
      fontSize,
      lineHeight: 1.3,
      letterSpacing: 0.3,
      theme: TERM_THEME,
      allowProposedApi: true,
      scrollback: 10000,
      scrollSensitivity: 2,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    termRef.current = term
    fitRef.current = fit

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
        // Proxies and extensions can inject non-JSON bytes; a malformed
        // frame is ignored so one bad message never kills the terminal.
        const frame = parseTerminalFrame(ev.data as string)
        if (!frame) return
        if (frame.type === 'output') term.write(frame.data ?? '')
        if (frame.type === 'exit') {
          onStatus('ended')
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
