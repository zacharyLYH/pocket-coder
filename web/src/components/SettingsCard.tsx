import { useState } from 'react'
import { Download, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { api, errMsg } from '@/lib/api'

export function SettingsCard({ onReset, onBusy }: {
  onReset?: () => void
  onBusy?: (b: boolean) => void
}) {
  const [busy, setBusy] = useState<'download' | 'wipe' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirmWipe, setConfirmWipe] = useState(false)

  async function downloadState() {
    setError(null)
    const res = await fetch('/api/state?download=true')
    if (!res.ok) throw new Error(`HTTP ${res.status}`)
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'state.json'
    document.body.appendChild(a)
    a.click()
    a.remove()
    URL.revokeObjectURL(url)
  }

  async function download() {
    setBusy('download')
    setError(null)
    onBusy?.(true)
    try {
      await downloadState()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
      onBusy?.(false)
    }
  }

  async function wipe() {
    setConfirmWipe(false)
    setBusy('wipe')
    setError(null)
    onBusy?.(true)
    try {
      await downloadState()
      await api<{ ok: boolean }>('/api/state', { method: 'DELETE' })
      onReset?.()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
      onBusy?.(false)
    }
  }

  return (
    <div data-testid="settings-card" className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">
        Export your state.json or wipe all data. Wiping downloads your state
        first, then deletes every project, harness, and session.
      </p>
      {error && <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="settings-error">{error}</p>}
      <div className="flex gap-2">
        <Button variant="outline" className="min-h-[44px] flex-1" disabled={busy !== null} onClick={() => void download()} data-testid="settings-download">
          <Download className="size-4" />{busy === 'download' ? 'Downloading…' : 'Download state'}
        </Button>
        <Button variant="destructive" className="min-h-[44px] flex-1" disabled={busy !== null} onClick={() => setConfirmWipe(true)} data-testid="settings-wipe">
          <Trash2 className="size-4" />Wipe all data
        </Button>
      </div>
      <AlertDialog open={confirmWipe} onOpenChange={(o) => { if (!o) setConfirmWipe(false) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Wipe all data?</AlertDialogTitle>
            <AlertDialogDescription>
              This deletes every project, harness, session, and thread. Your
              state.json will download automatically before the wipe. This
              cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel data-testid="settings-cancel">Cancel</AlertDialogCancel>
            <AlertDialogAction
              data-testid="settings-confirm-wipe"
              disabled={busy !== null}
              onClick={() => void wipe()}
            >
              {busy === 'wipe' ? 'Wiping…' : 'Wipe data'}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
