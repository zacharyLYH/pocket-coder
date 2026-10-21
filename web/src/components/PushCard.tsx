import { useCallback, useEffect, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errMsg } from '@/lib/api'
import {
  enable, isSupported, listSubscriptions, resync, sendTest,
  type PushState, type StoredSubscription,
} from '@/lib/push'

type State = PushState | 'checking'

export function PushCard() {
  const [state, setState] = useState<State>('checking')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [sent, setSent] = useState<'enabled' | 'test' | null>(null)
  const [devices, setDevices] = useState<StoredSubscription[]>([])

  const refreshDevices = useCallback(async () => {
    try {
      setDevices(await listSubscriptions())
    } catch {
      // The list is informational; the enable flow owns the error surface.
    }
  }, [])

  useEffect(() => {
    if (!isSupported()) {
      setState('unsupported')
      return
    }
    let alive = true
    void resync()
      .then((next) => { if (alive) setState(next) })
      .catch((err) => { if (alive) setState('default'); setError(errMsg(err)) })
    void refreshDevices()
    return () => { alive = false }
  }, [refreshDevices])

  async function run(task: () => Promise<void>) {
    setBusy(true)
    setError(null)
    try {
      await task()
    } catch (err) {
      setError(errMsg(err))
      setState(Notification.permission === 'denied' ? 'denied' : 'default')
    } finally {
      setBusy(false)
    }
  }

  function turnOn() {
    void run(async () => {
      await enable()
      setState('enabled')
      setSent('enabled')
      await refreshDevices()
    })
  }

  function test() {
    void run(async () => {
      await sendTest()
      setSent('test')
    })
  }

  const status = {
    checking: 'checking…',
    unsupported: 'not supported in this browser',
    denied: 'blocked in browser settings',
    default: 'off on this device',
    enabled: 'on for this device',
  }[state]
  const dot = state === 'enabled' ? 'bg-emerald-500'
    : state === 'denied' ? 'bg-red-500'
      : state === 'default' ? 'bg-amber-500' : 'bg-muted-foreground/50'

  return (
    <div className="flex flex-col gap-0.5" data-testid="push-card">
      <div className="flex min-h-[44px] items-center gap-2 px-2 py-1 text-[15px]">
        <span className={`size-2 shrink-0 rounded-full ${dot}`} aria-hidden />
        <span className="font-medium">Push notifications</span>
        <span className="truncate text-sm text-muted-foreground" data-testid="push-status">{status}</span>
        <span className="flex-1" />
        {state === 'default' && (
          <Button size="sm" onClick={turnOn} disabled={busy} data-testid="push-enable">
            {busy ? 'Enabling…' : 'Enable'}
          </Button>
        )}
        {state === 'enabled' && (
          <Button size="sm" variant="outline" onClick={test} disabled={busy} data-testid="push-test">
            {busy ? 'Sending…' : 'Send test'}
          </Button>
        )}
      </div>
      {state === 'enabled' && devices.length > 0 && (
        <p className="pl-6 text-xs text-muted-foreground" data-testid="push-devices">
          {devices.length} device{devices.length === 1 ? '' : 's'} registered
        </p>
      )}
      {state === 'denied' && (
        <p className="pl-6 text-xs text-muted-foreground" data-testid="push-denied">
          Allow notifications for Pocket Coder in your device settings, then reload. On iPhone, remove and re-add the Home Screen app if the setting does not appear.
        </p>
      )}
      {state === 'unsupported' && (
        <p className="pl-6 text-xs text-muted-foreground" data-testid="push-unsupported">
          This browser cannot receive push. On iPhone and Android, add Pocket Coder to your Home Screen first.
        </p>
      )}
      {error && (
        <p className="max-h-24 overflow-auto break-all pl-6 text-xs text-destructive" data-testid="push-error">{error}</p>
      )}
      <Dialog open={sent !== null} onOpenChange={(open) => { if (!open) setSent(null) }}>
        <DialogContent data-testid="push-success">
          <DialogHeader>
            <DialogTitle>{sent === 'test' ? 'Test notification sent' : 'Push notifications on'}</DialogTitle>
            <DialogDescription>
              This means we've successfully enabled push notifications for this device!
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button onClick={() => setSent(null)} data-testid="push-success-close">Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}
