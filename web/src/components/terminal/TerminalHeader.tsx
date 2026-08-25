import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Separator } from '@/components/ui/separator'
import type { ConnStatus } from '@/components/terminal/TerminalPane'

// The terminal header: back to projects, connection status, the session
// picker, and the new-session / restart / kill actions.
export function TerminalHeader({ projectId, current, sessions, status, onBack, onSwitch, onNewSession, onRestart, onKill }: {
  projectId: string
  current: string
  sessions: { name: string }[]
  status: ConnStatus
  onBack: () => void
  onSwitch: (name: string) => void
  onNewSession: () => void
  onRestart: () => void
  onKill: () => void
}) {
  const statusMeta = {
    connecting: { label: 'Connecting…', dot: 'bg-amber-500' },
    live: { label: 'Connected', dot: 'bg-emerald-500' },
    ended: { label: 'Disconnected', dot: 'bg-muted-foreground/40' },
  }[status]

  return (
    <header className="flex flex-wrap items-center gap-2 rounded-xl border bg-card px-3 py-2 shadow-sm">
      <Button variant="ghost" size="sm" onClick={onBack}>
        ← Projects
      </Button>
      <Separator orientation="vertical" className="data-[orientation=vertical]:h-5" />
      <Badge variant="outline" className="gap-1.5 border-border/60 bg-background font-normal">
        <span className={`size-1.5 rounded-full ${statusMeta.dot} ${status === 'live' ? 'animate-pulse' : ''}`} />
        {statusMeta.label}
      </Badge>
      <span className="font-mono text-xs text-muted-foreground">
        {projectId}
      </span>
      <span className="flex-1" />

      {/* Session picker */}
      <select
        className="h-8 rounded-md border border-input bg-transparent px-2 text-xs shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        value={current}
        onChange={(e) => onSwitch(e.target.value)}
        aria-label="Session"
      >
        {sessions.map((s) => (
          <option key={s.name} value={s.name}>{s.name}</option>
        ))}
        {!sessions.find((s) => s.name === current) && (
          <option value={current}>{current}</option>
        )}
      </select>

      <Button
        variant="secondary"
        size="sm"
        className="cursor-pointer"
        onClick={onNewSession}
      >
        + New Session
      </Button>
      <Button className="cursor-pointer" variant="outline" size="sm" onClick={onRestart}>
        Restart
      </Button>
      <Button
        variant="outline"
        size="sm"
        className="text-destructive cursor-pointer hover:bg-destructive/10 hover:text-destructive"
        onClick={onKill}
      >
        Kill
      </Button>
    </header>
  )
}
