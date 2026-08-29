import { useState } from 'react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import type { ConnStatus } from '@/components/terminal/TerminalPane'

// The terminal header: back to projects, connection status, the session
// picker, and the new-session / restart / rename / kill actions.
// The session picker is a custom dropdown (not a native <select>) so its
// open state is part of the DOM and can be captured in visual screenshots.
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
  const [pickerOpen, setPickerOpen] = useState(false)

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

  const allSessions = sessions.find((s) => s.name === current) ? sessions : [...sessions, { name: current }]

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

        {/* Session picker — custom dropdown so open state is visible in screenshots */}
        <div
          className="relative"
          onKeyDown={(e) => {
            if (e.key === 'Escape') setPickerOpen(false)
          }}
        >
          <button
            type="button"
            aria-label="Session"
            aria-expanded={pickerOpen}
            aria-haspopup="listbox"
            onClick={() => setPickerOpen((o) => !o)}
            className="flex h-8 min-w-[8rem] items-center justify-between gap-2 rounded-md border border-input bg-transparent px-2 text-xs shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            <span className="truncate">{current}</span>
            <span className="text-muted-foreground">▾</span>
          </button>
          {pickerOpen && (
            <div
              role="listbox"
              aria-label="Session list"
              className="absolute right-0 z-50 mt-1 max-h-60 min-w-[8rem] overflow-auto rounded-md border bg-popover p-1 shadow-md"
            >
              {allSessions.map((s) => (
                <button
                  key={s.name}
                  type="button"
                  role="option"
                  aria-selected={s.name === current}
                  onClick={() => {
                    setPickerOpen(false)
                    if (s.name !== current) onSwitch(s.name)
                  }}
                  className={`flex w-full items-center rounded-sm px-2 py-1.5 text-xs outline-none hover:bg-accent hover:text-accent-foreground ${s.name === current ? 'bg-accent text-accent-foreground' : ''}`}
                >
                  {s.name}
                </button>
              ))}
            </div>
          )}
        </div>

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
        <Button className="cursor-pointer" variant="outline" size="sm" onClick={openRename}>
          Rename
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
