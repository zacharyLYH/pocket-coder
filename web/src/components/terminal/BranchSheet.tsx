import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import type { GitBranchList } from '@/lib/types'

// Branch sheet (§5): bottom sheet listing local + remote branches with
// create. Pure presentational: switching state lives in DiffTab.
export function BranchSheet({ branches, gitBusy, newBranch, onNewBranch, hasUpstream, ahead, onClose, onSwitch }: {
  branches: GitBranchList | null
  gitBusy: string | null
  newBranch: string
  onNewBranch: (v: string) => void
  hasUpstream: boolean
  ahead: number
  onClose: () => void
  onSwitch: (name: string, create?: boolean) => void
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-end bg-black/40" onClick={onClose} data-testid="git-branch-sheet-backdrop">
      <div
        className="max-h-[70vh] w-full overflow-auto rounded-t-xl border bg-background p-3 pb-[env(safe-area-inset-bottom)]"
        onClick={(e) => e.stopPropagation()}
        data-testid="git-branch-sheet"
      >
        <p className="pb-2 text-sm font-medium">Branches</p>
        {branches && (
          <>
            <p className="pb-1 text-xs text-muted-foreground">Current: {branches.current}{branches.detached ? ' (detached)' : ''}</p>
            {[...branches.local].sort((a, b) => (a.name === branches.current ? -1 : b.name === branches.current ? 1 : 0)).map((b) => (
              <button
                key={b.name}
                className="flex min-h-[44px] w-full items-center justify-between px-2 text-left text-sm"
                onClick={() => void onSwitch(b.name)}
                disabled={gitBusy !== null || b.name === branches.current}
                data-testid="git-branch-local"
              >
                <span>{b.name}</span>
                <span className="text-xs text-muted-foreground">{b.relativeTime}</span>
              </button>
            ))}
            {branches.remote.map((b) => (
              <button
                key={b.name}
                className="flex min-h-[44px] w-full items-center justify-between px-2 text-left text-sm text-muted-foreground"
                onClick={() => void onSwitch(b.name)}
                disabled={gitBusy !== null}
                data-testid="git-branch-remote"
              >
                <span>origin/{b.name}</span>
                <span className="text-xs">{b.relativeTime}</span>
              </button>
            ))}
            <div className="mt-2 flex gap-2 border-t pt-2">
              <Input placeholder="New branch name" value={newBranch} onChange={(e) => onNewBranch(e.target.value)} data-testid="git-branch-new-name" />
              <Button
                size="sm"
                className="min-h-[44px]"
                disabled={gitBusy !== null || !newBranch.trim()}
                onClick={() => void onSwitch(newBranch.trim(), true)}
                data-testid="git-branch-create"
              >
                Create
              </Button>
            </div>
          </>
        )}
        <p className="pt-2 text-[11px] text-muted-foreground">Running sessions keep their terminal; the worktree moves.</p>
        {hasUpstream && ahead > 0 && (
          <p className="text-[11px] text-muted-foreground" data-testid="git-branch-unpushed-warning">
            {ahead} unpushed commit{ahead === 1 ? '' : 's'} stay{ahead === 1 ? 's' : ''} on this branch until you push.
          </p>
        )}
      </div>
    </div>
  )
}
