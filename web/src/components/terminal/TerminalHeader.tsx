import { useState } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { api, errMsg } from '@/lib/api'
import { useQuickCommands } from '@/hooks/useQuickCommands'
import { QuickCommandsModal } from '@/components/QuickCommandsModal'
import type { ConnStatus } from '@/components/terminal/TerminalPane'

// The terminal header: back to projects, connection status, the session
// picker, and the new-session / restart / rename / kill actions.
export function TerminalHeader({ projectId, current, sessions, status, onBack, onSwitch, onNewSession, onRestart, onRename, onKill }: {
  projectId: string
  current: string
  sessions: { name: string }[]
  status: ConnStatus
  onBack: () => void
  onSwitch: (name: string) => void
  onNewSession: () => void
  onRestart: () => void
  onRename: (newName: string) => Promise<void> | void
  onKill: () => void
}) {
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameValue, setRenameValue] = useState('')
  const [renameBusy, setRenameBusy] = useState(false)
  const [renameError, setRenameError] = useState<string | null>(null)
  const [qcOpen, setQcOpen] = useState(false)
  const { commands: quickCommands, reload: reloadQuickCommands } = useQuickCommands(projectId)
  const [injectError, setInjectError] = useState<string | null>(null)

  async function inject(command: string) {
    setInjectError(null)
    try {
      await api(`/api/projects/${projectId}/sessions/${current}/inject`, { method: 'POST', body: JSON.stringify({ command }) })
    } catch (e) {
      setInjectError(errMsg(e))
    }
  }

  const statusMeta = {
    connecting: { label: 'Connecting…', dot: 'bg-amber-500' },
    live: { label: 'Connected', dot: 'bg-emerald-500' },
    ended: { label: 'Disconnected', dot: 'bg-muted-foreground/40' },
  }[status]

  function openRename() {
    setRenameValue(current)
    setRenameError(null)
    setRenameOpen(true)
  }

  async function submitRename() {
    const trimmed = renameValue.trim()
    if (!trimmed || trimmed === current) return
    setRenameBusy(true)
    setRenameError(null)
    try {
      await onRename(trimmed)
      setRenameOpen(false)
    } catch (err: unknown) {
      setRenameError(err instanceof Error ? err.message : String(err))
    } finally {
      setRenameBusy(false)
    }
  }

  return (
    <>
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

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" aria-label="Session" className="min-w-[8rem] justify-between">
              <span className="truncate">{current}</span>
              <span className="text-muted-foreground">▾</span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            {sessions.map((s) => (
              <DropdownMenuItem
                key={s.name}
                onSelect={() => {
                  if (s.name !== current) onSwitch(s.name)
                }}
                data-current={s.name === current || undefined}
                className={s.name === current ? 'bg-accent text-accent-foreground' : ''}
              >
                {s.name}
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>

        <Button
          variant="secondary"
          size="sm"
          onClick={onNewSession}
        >
          + New Session
        </Button>

        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" data-testid="terminal-actions-trigger">Actions ▾</Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="w-64">
            <DropdownMenuItem onSelect={onRestart} data-testid="terminal-action-restart">Restart</DropdownMenuItem>
            <DropdownMenuItem onSelect={openRename} data-testid="terminal-action-rename">Rename</DropdownMenuItem>
            <DropdownMenuItem onSelect={onKill} data-testid="terminal-action-kill" className="text-destructive">Kill</DropdownMenuItem>
            <div className="my-1 h-px bg-border" />
            {Object.entries(quickCommands).length === 0 ? (
              <div className="px-2 py-1 text-xs text-muted-foreground">No quick commands</div>
            ) : (
              Object.entries(quickCommands).map(([alias, cmd]) => (
                <DropdownMenuItem key={alias} onSelect={() => inject(cmd)} data-testid={`qc-run-${alias}`}>
                  {alias}: {cmd.slice(0, 30)}
                </DropdownMenuItem>
              ))
            )}
            <DropdownMenuItem onSelect={() => setQcOpen(true)} data-testid="qc-manage">Update quick commands…</DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <QuickCommandsModal projectId={projectId} open={qcOpen} onOpenChange={setQcOpen} onSaved={reloadQuickCommands} />
      </header>
      {injectError && <p className="text-xs text-destructive" data-testid="qc-inject-error">{injectError}</p>}



      <Dialog open={renameOpen} onOpenChange={setRenameOpen}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Rename session</DialogTitle>
          </DialogHeader>
          <div className="flex flex-col gap-3">
            <Input
              value={renameValue}
              onChange={(e) => setRenameValue(e.target.value)}
              placeholder="New session name"
              autoFocus
              disabled={renameBusy}
            />
            {renameError && <p className="text-destructive text-xs break-all">{renameError}</p>}
          </div>
          <DialogFooter>
            <Button variant="ghost" disabled={renameBusy} onClick={() => setRenameOpen(false)}>Cancel</Button>
            <Button disabled={renameBusy || !renameValue.trim() || renameValue.trim() === current} onClick={submitRename}>
              {renameBusy ? 'Renaming…' : 'Rename'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
