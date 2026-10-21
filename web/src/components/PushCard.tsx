import { useCallback, useEffect, useState } from 'react'
import { Bell, BellOff } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { errMsg } from '@/lib/api'
import {
  enable, isSupported, listSubscriptions, resync, sendTest,
  type PushState, type StoredSubscription,
} from '@/lib/push'

type State = PushState | 'checking'

function hostOf(endpoint: string): string {
  try {
    return new URL(endpoint).host
  } catch {
    return endpoint
  }
}

export function PushCard({ onBusy }: { onBusy?: (b: boolean) => void }) {
  const [state, setState] = useState<State>('checking')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [devices, setDevices] = useState<StoredSubscription[]>([])
  const [sent, setSent] = useState<'enabled' | 'test' | null>(null)

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

  async function turnOn() {
    setBusy(true)
    setError(null)
    onBusy?.(true)
    try {
      await enable()
      setState('enabled')
      setSent('enabled')
      await refreshDevices()
    } catch (err) {
      setState(Notification.permission === 'denied' ? 'denied' : 'default')
      setError(errMsg(err))
    } finally {
      setBusy(false)
      onBusy?.(false)
    }
  }

  async function test() {
    setBusy(true)
    setError(null)
    try {
      await sendTest()
      setSent('test')
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card className="gap-3 py-4" data-testid="push-card">
      <CardHeader className="px-4">
        <CardTitle className="flex items-center gap-2 text-[17px] tracking-tight">
          {state === 'denied' ? <BellOff className="size-4" /> : <Bell className="size-4" />}
          Notifications
        </CardTitle>
        <CardDescription>
          {state === 'enabled' && 'This device gets a push when your agent needs you.'}
          {state === 'denied' && 'Notifications are blocked for this device.'}
          {state === 'unsupported' && 'This browser cannot receive web push.'}
          {(state === 'default' || state === 'checking') && 'Install to your Home Screen, then enable notifications on this device.'}
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 px-4">
        {state === 'checking' && (
          <p className="text-sm text-muted-foreground" data-testid="push-checking">Checking notification status…</p>
        )}
        {state === 'unsupported' && (
          <p className="text-sm text-muted-foreground" data-testid="push-unsupported">
            This browser does not support web push. On iPhone and Android, add Pocket Coder to your Home Screen first.
          </p>
        )}
        {state === 'denied' && (
          <p className="text-sm text-muted-foreground" data-testid="push-denied">
            Notifications are blocked. Allow them for Pocket Coder in your device settings, then reload this page.
            On iPhone, remove and re-add the Home Screen app if the setting does not appear.
          </p>
        )}
        {state === 'default' && (
          <Button onClick={() => void turnOn()} disabled={busy} className="min-h-[44px]" data-testid="push-enable">
            <Bell className="size-4" />{busy ? 'Enabling…' : 'Enable notifications'}
          </Button>
        )}
        {state === 'enabled' && (
          <Button variant="outline" onClick={() => void test()} disabled={busy} className="min-h-[44px]" data-testid="push-test">
            <Bell className="size-4" />{busy ? 'Sending…' : 'Send test notification'}
          </Button>
        )}
        {error && (
          <p className="max-h-24 overflow-auto break-all text-xs text-destructive" data-testid="push-error">{error}</p>
        )}
        {devices.length > 0 && state === 'enabled' && (
          <div className="flex flex-col gap-1" data-testid="push-devices">
            <p className="text-xs font-medium text-muted-foreground">
              {devices.length} subscribed device{devices.length === 1 ? '' : 's'}
            </p>
            {devices.map((device) => (
              <p key={device.endpoint} className="truncate text-xs text-muted-foreground" title={device.endpoint}>
                {hostOf(device.endpoint)}
              </p>
            ))}
          </div>
        )}
      </CardContent>
      <Dialog open={sent !== null} onOpenChange={(open) => { if (!open) setSent(null) }}>
        <DialogContent data-testid="push-success">
          <DialogHeader>
            <DialogTitle>{sent === 'test' ? 'Test notification sent' : 'Notifications on'}</DialogTitle>
            <DialogDescription>
              This means we've successfully enabled push notifications for this device!
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button onClick={() => setSent(null)} data-testid="push-success-close">Done</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}
