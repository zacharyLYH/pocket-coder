import { X } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'

export type FixedView = 'diff' | 'preview' | 'logs'

// TerminalTabs is the single browser-like tab strip: one tab per session,
// a "+ New Tab" opener, then the pinned Diff/Preview/Logs views. Session
// tabs size to their label (shrink-0, truncated at max-w) and the strip
// scrolls horizontally so many tabs stay reachable on narrow screens.
//
// The ✕ deletes the session outright (tmux kill + state.json metadata
// removed), so the tab disappears permanently — like closing a browser tab.
// On the last remaining session the ✕ is hidden: the UI cannot represent
// zero sessions.
export function TerminalTabs({ sessions, current, view, onSelectSession, onDeleteSession, onNewTab, onSelectView }: {
  sessions: { name: string }[]
  current: string
  view: 'terminal' | FixedView
  onSelectSession: (name: string) => void
  onDeleteSession: (name: string) => void
  onNewTab: () => void
  onSelectView: (view: FixedView) => void
}) {
  const fixed: { id: FixedView; label: string; testid: string }[] = [
    { id: 'diff', label: 'Diff', testid: 'tab-diff' },
    { id: 'preview', label: 'Preview', testid: 'tab-preview' },
    { id: 'logs', label: 'Logs', testid: 'tab-logs' },
  ]
  return (
    <div role="tablist" aria-label="Sessions and views" className="flex items-center gap-1.5 overflow-x-auto px-3 py-1">
      {sessions.map((s) => {
        const active = view === 'terminal' && s.name === current
        return (
          <div
            key={s.name}
            role="tab"
            aria-selected={active}
            data-testid={`tab-session-${s.name}`}
            onClick={() => onSelectSession(s.name)}
            className={`flex shrink-0 cursor-pointer items-center gap-1 rounded-md border px-2 py-1 text-sm whitespace-nowrap ${
              active
                ? 'border-foreground/40 bg-secondary text-secondary-foreground'
                : 'border-border/60 bg-card text-muted-foreground'
            }`}
          >
            <span className="max-w-36 truncate">{s.name}</span>
            {sessions.length > 1 && (
              <Button
                variant="ghost"
                size="icon"
                aria-label={`Close ${s.name}`}
                data-testid={`tab-close-${s.name}`}
                onClick={(e) => {
                  e.stopPropagation()
                  onDeleteSession(s.name)
                }}
                className="size-5 rounded p-0 text-muted-foreground hover:bg-accent hover:text-accent-foreground"
              >
                <X className="size-3.5" />
              </Button>
            )}
          </div>
        )
      })}
      <Button variant="ghost" size="sm" onClick={onNewTab} data-testid="tab-new" className="shrink-0">
        + New Tab
      </Button>
      <Separator orientation="vertical" className="data-[orientation=vertical]:h-5 shrink-0" />
      {fixed.map((f) => (
        <Button
          key={f.id}
          variant="outline"
          size="sm"
          role="tab"
          aria-selected={view === f.id}
          data-testid={f.testid}
          title="Pinned view — always available"
          onClick={() => onSelectView(f.id)}
          className={`shrink-0 cursor-pointer border-amber-500/60 whitespace-nowrap ${
            view === f.id
              ? 'bg-amber-500/25 text-foreground'
              : 'bg-amber-500/10 text-muted-foreground'
          }`}
        >
          {f.label}
        </Button>
      ))}
    </div>
  )
}
