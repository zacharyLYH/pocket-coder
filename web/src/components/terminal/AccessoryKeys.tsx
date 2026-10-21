import { useEffect } from 'react'
import { Button } from '@/components/ui/button'
import { pasteTermText, sendTermInput, TERM_SHORTCUTS_CHANGED_EVENT } from '@/components/terminal/QuickKeys'
import type { Shortcut } from '@/lib/shortcuts'
import { useShortcuts } from '@/hooks/useShortcuts'

const FIXED_KEYS = [
  { label: 'Esc', seq: '\x1b' },
  { label: 'Tab', seq: '\t' },
  { label: 'Shift-Tab', seq: '\x1b[Z' },
  { label: '←', aria: 'Left arrow', seq: '\x1b[D' },
  { label: '↑', aria: 'Up arrow', seq: '\x1b[A' },
  { label: '↓', aria: 'Down arrow', seq: '\x1b[B' },
  { label: '→', aria: 'Right arrow', seq: '\x1b[C' },
  { label: 'Ctrl-C', seq: '\x03' },
  { label: 'Ctrl-D', seq: '\x04' },
]

export function AccessoryKeys({ projectId, session, onRunShortcut }: {
  projectId: string
  session: string
  onRunShortcut: (shortcut: Shortcut) => void
}) {
  const { shortcuts, reload } = useShortcuts(projectId)
  const canPaste = typeof navigator !== 'undefined' && typeof navigator.clipboard?.readText === 'function'

  useEffect(() => {
    window.addEventListener(TERM_SHORTCUTS_CHANGED_EVENT, reload)
    return () => window.removeEventListener(TERM_SHORTCUTS_CHANGED_EVENT, reload)
  }, [reload])

  async function paste() {
    try {
      const clipboard = navigator.clipboard
      if (clipboard?.readText) pasteTermText(await clipboard.readText(), session)
    } catch {
      // Clipboard reads can be denied; leave the terminal untouched.
    }
  }

  return (
    <div className="mx-3 mb-2 rounded-xl border bg-card p-2 pb-[calc(0.5rem+env(safe-area-inset-bottom))]" data-testid="accessory-keys">
      <div className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto">
        {FIXED_KEYS.map((key) => (
          <Button
            key={key.label}
            type="button"
            variant="outline"
            aria-label={key.aria ?? key.label}
            title="Fixed terminal key"
            className="min-h-9 min-w-9 shrink-0 border-orange-300 bg-orange-100 px-2 font-mono text-xs text-orange-900 hover:bg-orange-200 dark:border-orange-300 dark:bg-orange-100 dark:text-orange-900 dark:hover:bg-orange-200"
            onClick={() => sendTermInput(key.seq, session)}
          >
            {key.label}
          </Button>
        ))}
        {shortcuts.map((shortcut) => (
          <Button
            key={shortcut.id}
            type="button"
            variant="outline"
            title="User-defined shortcut"
            className="min-h-9 min-w-9 shrink-0 border-green-300 bg-green-100 px-2 font-mono text-xs text-green-900 hover:bg-green-200 dark:border-green-300 dark:bg-green-100 dark:text-green-900 dark:hover:bg-green-200"
            onClick={() => onRunShortcut(shortcut)}
          >
            {shortcut.alias}
          </Button>
        ))}
        {canPaste && (
          <Button type="button" variant="secondary" className="min-h-9 shrink-0 px-3 text-xs" onClick={() => void paste()}>
            Paste
          </Button>
        )}
      </div>
    </div>
  )
}
