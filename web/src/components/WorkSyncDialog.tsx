import { useEffect, useState } from 'react'
import { Copy } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg, projectPath } from '@/lib/api'
import { useSyncStatus } from '@/hooks/useSyncStatus'
import type { SyncCode } from '@/lib/types'

// Work Sync: pcoder shows a short-lived code plus the laptop command. The
// laptop always starts the sync, so this dialog never moves files itself.
export function WorkSyncDialog({ projectId, open, onOpenChange }: {
  projectId: string; open: boolean; onOpenChange: (open: boolean) => void
}) {
  const [info, setInfo] = useState<SyncCode | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [remaining, setRemaining] = useState('')
  const [copied, setCopied] = useState(false)
  const status = useSyncStatus(projectId, open)

  useEffect(() => {
    if (!open) return
    let alive = true
    setInfo(null); setError(null); setCopied(false)
    api<SyncCode>(projectPath(projectId, '/sync/code'), { method: 'POST' })
      .then((d) => { if (alive) setInfo(d) })
      .catch((err) => { if (alive) setError(errMsg(err)) })
    return () => { alive = false }
  }, [open, projectId])

  // Countdown keeps "expires in" honest while the dialog stays open.
  useEffect(() => {
    if (!info) return
    const tick = () => setRemaining(countdown(info.expiresAt))
    tick()
    const id = setInterval(tick, 1000)
    return () => clearInterval(id)
  }, [info])

  async function copy() {
    if (!info) return
    try {
      await navigator.clipboard.writeText(info.code)
      setCopied(true)
    } catch {
      setCopied(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-md" data-testid="work-sync-dialog">
        <DialogHeader>
          <DialogTitle>Work Sync</DialogTitle>
          <DialogDescription>
            Move this project's working tree between this laptop and pcoder without a commit.
          </DialogDescription>
        </DialogHeader>

        {error && <p data-testid="sync-error" className="text-sm text-destructive">{error}</p>}
        {!error && !info && <p className="text-sm text-muted-foreground">Generating code…</p>}

        {info && (
          <>
            <div className="rounded-xl bg-muted px-4 py-5 text-center">
              <p className="text-xs uppercase tracking-wide text-muted-foreground">Sync code</p>
              <p data-testid="sync-code" className="mt-1 select-all font-mono text-2xl tracking-widest">{info.code}</p>
              <p data-testid="sync-expiry" className="mt-1 text-xs text-muted-foreground">{remaining || countdown(info.expiresAt)}</p>
            </div>

            <div className="flex flex-col gap-1">
              <p className="text-sm font-medium">On your laptop, run</p>
              <code data-testid="sync-command" className="block overflow-x-auto rounded-lg bg-muted px-3 py-2 font-mono text-xs">{info.command}</code>
              <div className="flex gap-2">
                <Button type="button" size="sm" variant="outline" onClick={copy} data-testid="sync-copy">
                  <Copy className="size-4" /> {copied ? 'Copied' : 'Copy code'}
                </Button>
              </div>
            </div>

            <p className="text-xs text-muted-foreground">
              <strong>Upload</strong> overwrites pcoder with this folder. <strong>Download</strong> overwrites this folder with pcoder.
              Both sides must be on the same branch and commit. Affected files are backed up first.
            </p>
            <p data-testid="sync-agent-warning" className="rounded-lg border border-dashed px-3 py-2 text-xs">
              <strong>Stop agent turns in this project first.</strong> A turn writing mid-transfer fails the sync —
              pcoder cannot detect a running agent, and both sides are overwritten, not merged.
            </p>
            <p data-testid="sync-status" data-status={status} className="text-sm font-medium">
              {status === 'idle'
                ? 'Waiting for your laptop…'
                : `${status === 'uploading' ? 'Uploading' : 'Downloading'}… don't touch this project until it finishes.`}
            </p>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}

// countdown renders "expires in 9:58"; expired codes say so plainly.
function countdown(expiresAt: string): string {
  const ms = new Date(expiresAt).getTime() - Date.now()
  if (!Number.isFinite(ms) || ms <= 0) return 'Expired — generate a new code'
  const s = Math.floor(ms / 1000)
  return `Expires in ${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}
