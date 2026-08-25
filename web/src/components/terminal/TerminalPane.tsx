import { useEffect, type RefObject } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

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
// in — the server owns the protocol (internal/httpapi/terminal.go).
// Re-dials whenever session or redial changes; the host element is owned by
// the parent view.
export function TerminalPane({ projectId, session, redial, hostRef, onStatus, onError }: {
  projectId: string
  session: string
  redial: number
  hostRef: RefObject<HTMLDivElement | null>
  onStatus: (status: ConnStatus) => void
  onError: (message: string) => void
}) {
  useEffect(() => {
    let disposed = false
    let opened = false
    let ws: WebSocket | null = null

    const term = new Terminal({
      cursorBlink: true,
      cursorStyle: 'bar',
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, "Cascadia Code", "Fira Code", monospace',
      fontSize: 14,
      lineHeight: 1.3,
      letterSpacing: 0.3,
      theme: TERM_THEME,
      allowProposedApi: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)

    const observer = new ResizeObserver(() => {
      if (!opened || !ws) return
      fit.fit()
      sendResize(ws, fit)
    })

    async function ensureSessionThenDial() {
      onStatus('connecting')
      try {
        const res = await fetch(`/api/projects/${projectId}/sessions`, {
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
      ws = new WebSocket(`${proto}://${location.host}/ws/projects/${projectId}/sessions/${session}`)

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
        const frame = JSON.parse(ev.data as string) as { type: string; data?: string; code?: number }
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
      term.dispose()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, session, redial])

  return null
}

function sendResize(ws: WebSocket, fit: FitAddon) {
  if (ws.readyState !== WebSocket.OPEN) return
  const dims = fit.proposeDimensions()
  if (dims && dims.rows > 0 && dims.cols > 0) {
    ws.send(JSON.stringify({ type: 'resize', rows: dims.rows, cols: dims.cols }))
  }
}
