import { useState } from 'react'
import { Moon, Sun, ZoomIn, ZoomOut } from 'lucide-react'

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
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useTheme } from '@/components/ThemeToggle'
import type { ConnStatus } from '@/components/terminal/TerminalPane'

// The terminal header: back to projects, connection status, the current
// session name, and the restart / rename / kill actions. Session
// switching lives in the tab strip below (TerminalTabs), not here.
// Permanent deletion is handled by the ✕ close button on each tab.
//
// When a pinned view (Codemap/Git/Preview/Nerdy Stuff) is active the header
// switches to fixed-view mode: no connection badge, no session name, and
// no Actions menu — session actions act on the attached terminal session,
// which is not visible while transported into a fixed view.
const FIXED_LABELS: Record<string, string> = { codemap: 'Codemap', diff: 'Git', preview: 'Preview', nerdy: 'Nerdy Stuff' }

export function TerminalHeader({ projectId, current, status, view, fontSize, onBack, onRestart, onRename, onKill, onZoomIn, onZoomOut, onZoomReset }: {
  projectId: string
  current: string
  status: ConnStatus
  view: 'terminal' | 'diff' | 'preview' | 'nerdy' | 'codemap'
  fontSize: number
  onBack: () => void
  onRestart: () => void
  onRename: (newName: string) => Promise<void> | void
  onKill: () => void
  onZoomIn: () => void
  onZoomOut: () => void
  onZoomReset: () => void
}) {
  const isFixedView = view !== 'terminal'
  const fixedLabel = isFixedView ? (FIXED_LABELS[view] ?? view) : ''
  const [renameOpen, setRenameOpen] = useState(false)
  const [renameValue, setRenameValue] = useState('')
  const [renameBusy, setRenameBusy] = useState(false)
  const [renameError, setRenameError] = useState<string | null>(null)
  const [dark, setDark] = useTheme()

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
        {!isFixedView && (
          <Badge variant="outline" className="gap-1.5 border-border/60 bg-background font-normal">
            <span className={`size-1.5 rounded-full ${statusMeta.dot} ${status === 'live' ? 'animate-pulse' : ''}`} />
            {statusMeta.label}
          </Badge>
        )}
        <span className="font-mono text-xs text-muted-foreground">
          {projectId}
        </span>
        {isFixedView ? (
          <span className="truncate text-xs font-medium" data-testid="fixed-view-title" title={fixedLabel}>
            {fixedLabel}
          </span>
        ) : (
          <span className="truncate font-mono text-xs" data-testid="current-session" title={current}>
            {current}
          </span>
        )}
        <span className="flex-1" />

        {!isFixedView && (
          <DropdownMenu>
            <DropdownMenuTrigger asChild>
              <Button variant="outline" size="sm" data-testid="terminal-actions-trigger">Actions ▾</Button>
            </DropdownMenuTrigger>
            <DropdownMenuContent align="end" className="w-48">
              <DropdownMenuItem onSelect={onRestart} data-testid="terminal-action-restart">Restart</DropdownMenuItem>
              <DropdownMenuItem onSelect={openRename} data-testid="terminal-action-rename">Rename</DropdownMenuItem>
              <DropdownMenuItem onSelect={onKill} data-testid="terminal-action-kill" className="text-destructive">Kill</DropdownMenuItem>
              <DropdownMenuSeparator />
              <DropdownMenuSub>
                <DropdownMenuSubTrigger data-testid="terminal-action-zoom">Text size</DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="w-40">
                  <DropdownMenuItem onSelect={onZoomIn} disabled={fontSize >= 20} data-testid="term-zoom-in">
                    <ZoomIn className="size-4" /> Zoom in
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={onZoomOut} disabled={fontSize <= 10} data-testid="term-zoom-out">
                    <ZoomOut className="size-4" /> Zoom out
                  </DropdownMenuItem>
                  <DropdownMenuItem onSelect={onZoomReset} disabled={fontSize === 14} data-testid="term-zoom-reset">
                    Reset
                  </DropdownMenuItem>
                </DropdownMenuSubContent>
              </DropdownMenuSub>
              <DropdownMenuSub>
                <DropdownMenuSubTrigger data-testid="terminal-action-theme">Appearance</DropdownMenuSubTrigger>
                <DropdownMenuSubContent className="w-40">
                  <DropdownMenuRadioGroup value={dark ? 'dark' : 'light'} onValueChange={(v) => setDark(v === 'dark')}>
                    <DropdownMenuRadioItem value="light" data-testid="terminal-theme-light">
                      <Sun className="size-4" /> Light
                    </DropdownMenuRadioItem>
                    <DropdownMenuRadioItem value="dark" data-testid="terminal-theme-dark">
                      <Moon className="size-4" /> Dark
                    </DropdownMenuRadioItem>
                  </DropdownMenuRadioGroup>
                </DropdownMenuSubContent>
              </DropdownMenuSub>
            </DropdownMenuContent>
          </DropdownMenu>
        )}
      </header>



      {!isFixedView && (
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
      )}
    </>
  )
}
