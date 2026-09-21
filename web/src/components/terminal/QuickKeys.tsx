import { useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'

// Event the strip fires; TerminalPane forwards the sequence to the PTY
// over the existing websocket input path (no new transport).
export const TERM_INPUT_EVENT = 'pcoder-term-input'

export function sendTermInput(seq: string) {
  window.dispatchEvent(new CustomEvent<string>(TERM_INPUT_EVENT, { detail: seq }))
}

// Soft keyboards lack these: Esc, Tab, arrows, Ctrl-C/D. Sequences are
// what xterm would have sent for the physical key.
const KEYS: { label: string; seq: string; testid: string }[] = [
  { label: 'Esc', seq: '\x1b', testid: 'qk-esc' },
  { label: 'Tab', seq: '\t', testid: 'qk-tab' },
  { label: '↑', seq: '\x1b[A', testid: 'qk-up' },
  { label: '↓', seq: '\x1b[B', testid: 'qk-down' },
  { label: '←', seq: '\x1b[D', testid: 'qk-left' },
  { label: '→', seq: '\x1b[C', testid: 'qk-right' },
  { label: 'Ctrl-C', seq: '\x03', testid: 'qk-ctrl-c' },
  { label: 'Ctrl-D', seq: '\x04', testid: 'qk-ctrl-d' },
]

// QuickKeys is the horizontally scrollable chip row above the terminal
// input area. Ctrl is a sticky toggle: arm it, then the next character
// keypress sends its ctrl variant (c → ETX). The strip drives PTY input
// only, so it hides whenever a non-terminal text input owns the focus
// (message box, notes, dialogs) — tapping there must not inject keys.
export function QuickKeys() {
  const [ctrl, setCtrl] = useState(false)
  const [visible, setVisible] = useState(true)

  useEffect(() => {
    const update = () => {
      const el = document.activeElement as HTMLElement | null
      setVisible(
        !(el && (el.tagName === 'INPUT' || (el.tagName === 'TEXTAREA' && !el.classList.contains('xterm-helper-textarea')))),
      )
    }
    update()
    document.addEventListener('focusin', update)
    document.addEventListener('focusout', update)
    return () => {
      document.removeEventListener('focusin', update)
      document.removeEventListener('focusout', update)
    }
  }, [])

  // Sticky Ctrl: capture the next character key before xterm sees it and
  // send the ctrl variant instead. Modifiers already held pass through.
  useEffect(() => {
    if (!ctrl || !visible) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key.length !== 1 || e.ctrlKey || e.metaKey || e.altKey) return
      if (!/^[a-zA-Z]$/.test(e.key)) return
      e.preventDefault()
      e.stopPropagation()
      setCtrl(false)
      sendTermInput(String.fromCharCode(e.key.toUpperCase().charCodeAt(0) & 0x1f))
    }
    window.addEventListener('keydown', onKey, { capture: true })
    return () => window.removeEventListener('keydown', onKey, { capture: true })
  }, [ctrl, visible])

  if (!visible) return null
  return (
    <div className="flex gap-1.5 overflow-x-auto px-3 py-1" data-testid="quick-keys">
      <Button
        variant={ctrl ? 'default' : 'outline'}
        size="sm"
        aria-pressed={ctrl}
        onClick={() => setCtrl((v) => !v)}
        data-testid="qk-ctrl"
        className="min-h-[44px] shrink-0"
      >
        Ctrl
      </Button>
      {KEYS.map((k) => (
        <Button
          key={k.testid}
          variant="outline"
          size="sm"
          onClick={() => {
            setCtrl(false)
            sendTermInput(k.seq)
          }}
          data-testid={k.testid}
          className="min-h-[44px] shrink-0"
        >
          {k.label}
        </Button>
      ))}
    </div>
  )
}
