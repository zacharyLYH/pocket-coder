import { useEffect, useRef, type RefObject } from 'react'
import { FitAddon } from '@xterm/addon-fit'
import { Terminal } from '@xterm/xterm'
import '@xterm/xterm/css/xterm.css'

import { projectPath } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { parseTerminalFrame } from '@/lib/terminalFrame'
import {
  TERM_INPUT_EVENT,
  TERM_PASTE_EVENT,
  type TermInputDetail,
  type TermPasteDetail,
} from '@/components/terminal/QuickKeys'
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
export function TerminalPane({ projectId, session, redial, fontSize, hostRef, onStatus, onError, onCopied }: {
  projectId: string
  session: string
  redial: number
  fontSize: number
  hostRef: RefObject<HTMLDivElement | null>
  onStatus: (status: ConnStatus) => void
  onError: (message: string) => void
  onCopied?: () => void
}) {
  // Live socket for synthetic input (shortcuts modal's key buttons).
  const wsRef = useRef<WebSocket | null>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  // Set by the mount effect below; touch handlers call it while the gesture
  // is still active (Safari only grants clipboard writes during a user
  // gesture).
  const copyTerminalRef = useRef<() => void>(() => {})

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
    const LONG_PRESS_MS = 500
    const MOVE_TOLERANCE = 10
    const WORD_CHAR = /[A-Za-z0-9_./:@~+-]/
    let active = false
    let selecting = false
    let startX = 0
    let startY = 0
    let lastY = 0
    let accum = 0
    let longPress = 0
    let anchor: { col: number; row: number } | null = null
    let word: { start: number; end: number } | null = null

    // Map a screen point to a buffer cell. xterm exposes no hit-testing API
    // and the pane is canvas-rendered, so derive the cell from the live
    // screen box and the terminal's own cols/rows.
    function cellAt(clientX: number, clientY: number): { col: number; row: number } | null {
      const term = termRef.current
      const screen = host.querySelector('.xterm-screen') as HTMLElement | null
      const rect = (screen ?? host).getBoundingClientRect()
      if (!term || rect.width <= 0 || rect.height <= 0) return null
      const cols = term.cols || 80
      const rows = term.rows || 24
      const viewportRow = Math.min(rows - 1, Math.max(0, Math.floor(((clientY - rect.top) / rect.height) * rows)))
      const col = Math.min(cols - 1, Math.max(0, Math.floor(((clientX - rect.left) / rect.width) * cols)))
      return { col, row: term.buffer.active.viewportY + viewportRow }
    }

    function selectWordAt(clientX: number, clientY: number): boolean {
      const term = termRef.current
      const at = cellAt(clientX, clientY)
      if (!term || !at) return false
      const line = term.buffer.active.getLine(at.row)
      if (!line) return false
      const text = line.translateToString(true)
      let start = Math.min(at.col, text.length)
      let end = start
      if (text[start] && WORD_CHAR.test(text[start])) {
        while (start > 0 && WORD_CHAR.test(text[start - 1])) start--
        while (end < text.length && WORD_CHAR.test(text[end])) end++
      } else {
        end = Math.min(text.length, start + 1)
      }
      if (end <= start) return false
      anchor = { col: start, row: at.row }
      word = { start, end }
      term.select(start, at.row, end - start)
      try { navigator.vibrate?.(10) } catch { /* unsupported */ }
      return true
    }

    function extendSelection(clientX: number, clientY: number) {
      const term = termRef.current
      const at = cellAt(clientX, clientY)
      if (!term || !at || !anchor || !word) return
      // Drag past the word's start extends from its end backwards; anywhere
      // else extends forward from its start, like native word handles.
      const backwards = at.row < anchor.row || (at.row === anchor.row && at.col < word.start)
      const start = backwards ? at : { col: word.start, row: anchor.row }
      const end = backwards ? { col: word.end, row: anchor.row } : { col: Math.max(at.col + 1, word.end), row: at.row }
      const cols = term.cols || 80
      term.select(start.col, start.row, (end.row - start.row) * cols + (end.col - start.col))
    }

    function clearLongPress() {
      window.clearTimeout(longPress)
      longPress = 0
    }

    function resetGesture() {
      active = false
      selecting = false
      anchor = null
      word = null
      accum = 0
      clearLongPress()
    }

    function onTouchStart(e: TouchEvent) {
      resetGesture()
      // A fresh tap elsewhere drops the previous highlight, like native
      // text selection.
      termRef.current?.clearSelection()
      if (e.touches.length !== 1) return
      const t = e.touches[0]
      active = true
      startX = t.clientX
      startY = lastY = t.clientY
      // Native-style long press: select the word under the finger, then let
      // the same drag extend the selection. xterm has no touch selection of
      // its own and the pane renders to a canvas, so this is the only way a
      // phone user can select terminal text.
      longPress = window.setTimeout(() => {
        longPress = 0
        if (!active || selecting) return
        if (selectWordAt(startX, startY)) {
          selecting = true
          active = false
        }
      }, LONG_PRESS_MS)
    }

    function onTouchMove(e: TouchEvent) {
      if (!active && !selecting) return
      if (e.touches.length !== 1) return
      const t = e.touches[0]
      if (selecting) {
        e.preventDefault()
        extendSelection(t.clientX, t.clientY)
        return
      }
      const dx = Math.abs(t.clientX - startX)
      const dy = Math.abs(t.clientY - startY)
      if (longPress && (dx > MOVE_TOLERANCE || dy > MOVE_TOLERANCE)) clearLongPress()
      // Let horizontal gestures (rare) pass through untouched.
      if (dx > dy) return
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

    function onTouchEnd() {
      if (selecting) {
        selecting = false
        anchor = null
        word = null
      }
      active = false
      accum = 0
      clearLongPress()
      // Copy inside the gesture: Safari rejects clipboard writes that
      // happen after the transient activation expires. No-op when nothing
      // is selected.
      copyTerminalRef.current()
    }

    function onTouchCancel() {
      resetGesture()
    }

    function onContextMenu(e: Event) {
      // Suppress the mobile callout/context menu during a long press; the
      // accessory bar owns Paste.
      if (longPress || selecting) e.preventDefault()
    }

    host.addEventListener('touchstart', onTouchStart, { passive: true })
    host.addEventListener('touchmove', onTouchMove, { passive: false })
    host.addEventListener('touchend', onTouchEnd, { passive: true })
    host.addEventListener('touchcancel', onTouchCancel, { passive: true })
    host.addEventListener('contextmenu', onContextMenu)
    return () => {
      clearLongPress()
      host.removeEventListener('touchstart', onTouchStart)
      host.removeEventListener('touchmove', onTouchMove)
      host.removeEventListener('touchend', onTouchEnd)
      host.removeEventListener('touchcancel', onTouchCancel)
      host.removeEventListener('contextmenu', onContextMenu)
    }
  }, [hostRef])

  useEffect(() => {
    const onInput = (e: Event) => {
      const { seq, session: target } = (e as CustomEvent<TermInputDetail>).detail
      if (target && target !== session) return
      const ws = wsRef.current
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'input', data: seq }))
      }
    }
    window.addEventListener(TERM_INPUT_EVENT, onInput)
    const onPaste = (e: Event) => {
      const { text, session: target } = (e as CustomEvent<TermPasteDetail>).detail
      if (target && target !== session) return
      termRef.current?.paste(text)
    }
    window.addEventListener(TERM_PASTE_EVENT, onPaste)
    return () => {
      window.removeEventListener(TERM_INPUT_EVENT, onInput)
      window.removeEventListener(TERM_PASTE_EVENT, onPaste)
    }
  }, [session])

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

    const host = hostRef.current
    let copyTimer = 0
    // One highlight can fire two copies (the explicit release, then the
    // debounced selection-change fallback committing after mouseup). The last
    // write is remembered so the second one is skipped: no duplicate
    // clipboard write, no second "Copied" toast.
    let lastCopied = ''

    // Desktop drags in prod belong to tmux: ThemeArgs sets `mouse on`, so
    // xterm forwards the gesture as SGR mouse reports and its own selection
    // stays empty. tmux copies the highlighted text into its paste buffer,
    // which the server exposes at .../selection. Read it after the release
    // and copy while Chrome's transient activation is still alive. The
    // Fresh flag keeps an old buffer from being pasted after a drag a TUI
    // consumed (no tmux copy happened).
    async function tmuxSelection(): Promise<string> {
      // Give tmux a beat to finish copy-selection-and-cancel on mouseup.
      await new Promise((resolve) => setTimeout(resolve, 80))
      try {
        const res = await fetch(projectPath(projectId, `/sessions/${encodeURIComponent(session)}/selection`))
        if (!res.ok) return ''
        const data = await res.json() as { text?: unknown; fresh?: unknown }
        return data.fresh === true && typeof data.text === 'string' ? data.text : ''
      } catch {
        return ''
      }
    }

    // Highlight-to-copy. Three selection sources exist in the wild: xterm's
    // own canvas selection, the browser's native selection over xterm's
    // accessibility tree, and tmux's paste buffer (desktop, `mouse on`, where
    // the drag never reaches xterm). Copy on gesture end while user activation
    // is still alive; the debounced selection-change copy below covers a
    // browser that commits the range late.
    async function copyTerminal(allowTmux = false) {
      window.clearTimeout(copyTimer)
      let text = term.getSelection()
      if (!text) {
        const native = window.getSelection()
        if (native && native.rangeCount > 0 && host?.contains(native.anchorNode)) text = native.toString()
      }
      if (!text && allowTmux) text = await tmuxSelection()
      if (!text || text === lastCopied) return
      // Claim the highlight before awaiting: a second trigger landing
      // mid-write must not write (or toast) again.
      lastCopied = text
      const range = term.getSelectionPosition()
      if (!(await copyToClipboard(text))) return
      onCopied?.()
      // The execCommand fallback focuses a scratch textarea, which can clear
      // xterm's highlight. Put it back so the selection stays visible after
      // an automatic copy.
      if (range && !term.hasSelection()) {
        const cols = term.cols || 80
        term.select(range.start.x, range.start.y, (range.end.y - range.start.y) * cols + (range.end.x - range.start.x))
      }
    }
    copyTerminalRef.current = () => { void copyTerminal() }

    const selection = term.onSelectionChange(() => {
      window.clearTimeout(copyTimer)
      copyTimer = window.setTimeout(() => { void copyTerminal() }, 120)
    })

    // A mouseup can land outside the terminal after a drag past its edge, so
    // the release is read on the document. The drag distance decides whether
    // tmux is even a candidate: a plain click must NOT fall back to tmux,
    // because the newest buffer may still be fresh from an earlier drag even
    // though this gesture copied nothing.
    let mouseDownAt: { x: number; y: number } | null = null
    function onMouseDown(e: MouseEvent) {
      if (e.button !== 0) return
      mouseDownAt = { x: e.clientX, y: e.clientY }
      // tmux (mouse mode) owns this drag, so a local highlight from a
      // previous gesture would be stale. Without mouse mode xterm builds
      // its own selection; clearing it here would cancel that drag.
      if (term.modes.mouseTrackingMode !== 'none') term.clearSelection()
    }
    function onMouseUp(e: MouseEvent) {
      const from = mouseDownAt
      mouseDownAt = null
      if (!from) return
      void copyTerminal(Math.abs(e.clientX - from.x) > 3 || Math.abs(e.clientY - from.y) > 3)
    }
    host?.addEventListener('mousedown', onMouseDown)
    document.addEventListener('mouseup', onMouseUp)

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
      window.clearTimeout(copyTimer)
      selection.dispose()
      host?.removeEventListener('mousedown', onMouseDown)
      document.removeEventListener('mouseup', onMouseUp)
      copyTerminalRef.current = () => {}
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
