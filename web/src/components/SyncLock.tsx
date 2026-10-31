import { Loader2 } from 'lucide-react'

import type { SyncStatus } from '@/hooks/useSyncStatus'
import { cn } from '@/lib/utils'

// SyncLock is the read-only veil over a project while the laptop uploads or
// downloads. Work Sync overwrites, so touching the tree mid-transfer would
// corrupt it.
export function SyncLock({ status, className, fullscreen = false }: {
  status: SyncStatus; className?: string; fullscreen?: boolean
}) {
  if (status === 'idle') return null
  const label = status === 'uploading' ? 'Uploading' : 'Downloading'
  return (
    <div
      role="alert"
      data-testid="project-sync-lock"
      data-status={status}
      className={cn(
        'flex items-center justify-center gap-3 bg-background/85 p-4 text-center backdrop-blur-sm',
        fullscreen ? 'fixed inset-0 z-40' : 'absolute inset-0 z-10 rounded-xl',
        className,
      )}
    >
      <Loader2 className="size-4 animate-spin text-muted-foreground" />
      <div>
        <p className="text-sm font-medium">{label}…</p>
        <p className="text-xs text-muted-foreground">Wait for the sync to finish before using this project.</p>
      </div>
    </div>
  )
}
