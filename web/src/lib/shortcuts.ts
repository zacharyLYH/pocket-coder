export type ShortcutKind = 'cmd' | 'keys'
export type Shortcut = { id: string; alias: string; kind: ShortcutKind; command?: string; keys?: string }

export const ALIAS_RE = /^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/

// Human-readable key combo typed on mobile keyboards ("Ctrl-C", "Esc",
// "Up", "Tab") to the control sequence xterm would have sent. Anything
// unrecognized passes through verbatim so power users can paste raw
// sequences like \x1b[A.
const NAMED: Record<string, string> = {
  esc: '\x1b', escape: '\x1b', tab: '\t',
  up: '\x1b[A', down: '\x1b[B', left: '\x1b[D', right: '\x1b[C',
  enter: '\r', backspace: '\x7f',
}

export function parseKeyCombo(raw: string): string {
  const t = raw.trim()
  const low = t.toLowerCase()
  if (NAMED[low]) return NAMED[low]
  // Ctrl-C, Cmd-C (macOS ⌘ maps to Meta), Ctrl+C — all mean ETX.
  const m = /^(ctrl|control|cmd|command|meta)[-+\s]?([a-z])$/i.exec(t)
  if (m) return String.fromCharCode(m[2].toUpperCase().charCodeAt(0) & 0x1f)
  return t
}

// keydownToLabel turns a physical keypress into the human-readable combo
// the keys textbox stores ("Ctrl-C", "Esc", "Up"). Returns null for plain
// typing (letters without modifiers, Backspace alone) so the input keeps
// its normal behavior. Cmd (Meta) is treated as Ctrl — on macOS keyboards
// ⌘C must mean Ctrl-C, not the copy hotkey.
export function keydownToLabel(e: KeyboardEvent | React.KeyboardEvent): string | null {
  const mod = e.ctrlKey || e.metaKey
  const named: Record<string, string> = {
    Escape: 'Esc', Tab: 'Tab', Enter: 'Enter', Backspace: 'Backspace',
    ArrowUp: 'Up', ArrowDown: 'Down', ArrowLeft: 'Left', ArrowRight: 'Right',
  }
  if (named[e.key]) {
    if (e.key === 'Backspace' && !mod) return null
    e.preventDefault()
    return mod ? `Ctrl-${named[e.key]}` : named[e.key]
  }
  if (mod && /^[a-zA-Z]$/.test(e.key)) {
    e.preventDefault()
    return `Ctrl-${e.key.toUpperCase()}`
  }
  return null
}

export function looksLikeKeys(text: string): boolean {
  const t = text.trim().toLowerCase()
  if (!t) return false
  if (NAMED[t]) return true
  return /^(ctrl|control|cmd|command|meta)[-+\s]?([a-z]|esc|tab|enter|backspace|up|down|left|right)$/i.test(text.trim())
}

// shortcutText is the single display/edit value for a server row,
// whichever kind it is.
export function shortcutText(s: Shortcut): string {
  return s.kind === 'keys' ? (s.keys ?? '') : (s.command ?? '')
}

// toServerRow converts the unified {name, text} form back to the server
// shape, resolving cmd vs keys from the text so the UI never asks.
export function toServerRow(d: { id?: string; name: string; text: string }): Shortcut {
  const alias = d.name.trim()
  const text = d.text.trim()
  if (looksLikeKeys(text)) return { id: d.id || `s-${Date.now()}`, alias, kind: 'keys', keys: text }
  return { id: d.id || `s-${Date.now()}`, alias, kind: 'cmd', command: text }
}

// validateDraft checks one add/edit form against the existing rows.
// Returns the cleaned {name, text} or throws the first problem.
export function validateDraft(d: { name: string; text: string }, rows: Shortcut[], ignoreId?: string): { name: string; text: string } {
  const name = d.name.trim()
  const text = d.text.trim()
  if (!name) throw new Error('name must be non-empty')
  if (!ALIAS_RE.test(name)) throw new Error(`invalid name: ${name}`)
  if (rows.some((r) => r.alias === name && r.id !== ignoreId)) throw new Error(`duplicate name: ${name}`)
  if (!text) throw new Error('value must be non-empty')
  return { name, text }
}

// validateShortcutRows mirrors the server's checks so saves fail fast
// without a round trip. Returns cleaned rows or throws the first problem.
export function validateShortcutRows(rows: Shortcut[]): Shortcut[] {
  const seen = new Set<string>()
  return rows.map((r, i) => {
    const alias = r.alias.trim()
    if (!alias) throw new Error('alias must be non-empty')
    if (!ALIAS_RE.test(alias)) throw new Error(`invalid alias: ${alias}`)
    if (seen.has(alias)) throw new Error(`duplicate alias: ${alias}`)
    seen.add(alias)
    if (r.kind === 'cmd') {
      const command = (r.command ?? '').trim()
      if (!command) throw new Error('command must be non-empty')
      return { id: r.id || `s-${i}`, alias, kind: r.kind, command }
    }
    if (r.kind === 'keys') {
      const keys = (r.keys ?? '').trim()
      if (!keys) throw new Error('keys must be non-empty')
      return { id: r.id || `s-${i}`, alias, kind: r.kind, keys }
    }
    throw new Error('kind must be cmd or keys')
  })
}
