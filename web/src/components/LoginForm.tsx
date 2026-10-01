import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { usePwaInstall } from '@/hooks/usePwaInstall'

// Email to PIN login against the real backend. The PIN arrives by email
// (or the server console in dev/e2e).
export function LoginForm({
  onLoggedIn,
  navigate,
}: {
  onLoggedIn: (email: string) => void
  navigate?: (to: string) => void
}) {
  const [email, setEmail] = useState('')
  const [pin, setPin] = useState('')
  const [step, setStep] = useState<'email' | 'pin'>('email')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function requestPin(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/auth/request-pin', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      setStep('pin')
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  async function verify(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/auth/verify', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, pin }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      onLoggedIn(email)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <main className="bg-muted/40 grid min-h-dvh place-items-center gap-4 p-4">
      <div className="flex w-full max-w-sm flex-col gap-4">
        <div className="flex items-center gap-2">
          <img src="/icons/icon-192.png" alt="Pocket Coder" className="size-7 rounded-md" />
          <Button variant="link" size="sm" className="px-0" onClick={() => navigate?.('/')}>
            What is Pocket Coder?
          </Button>
        </div>
        <Card className="w-full">
          <CardHeader>
            <CardTitle>Pocket Coder</CardTitle>
            <CardDescription>Sign in with the PIN sent to your email.</CardDescription>
          </CardHeader>
          <CardContent>
            {step === 'email' ? (
              <form onSubmit={requestPin} className="flex flex-col gap-3">
                <Input
                  type="email"
                  required
                  autoComplete="email"
                  placeholder="you@example.com"
                  value={email}
                  onChange={(e) => setEmail(e.target.value)}
                />
                {error && <p className="text-destructive text-sm">{error}</p>}
                <Button type="submit" disabled={busy}>
                  Send code
                </Button>
              </form>
            ) : (
              <form onSubmit={verify} className="flex flex-col gap-3">
                <Input
                  type="text"
                  inputMode="numeric"
                  pattern="[0-9]*"
                  maxLength={6}
                  required
                  autoFocus
                  placeholder="6-digit PIN"
                  value={pin}
                  onChange={(e) => setPin(e.target.value)}
                />
                {error && <p className="text-destructive text-sm">{error}</p>}
                <Button type="submit" disabled={busy}>
                  Log in
                </Button>
                <Button type="button" variant="ghost" disabled={busy} onClick={() => setStep('email')}>
                  Back
                </Button>
              </form>
            )}
          </CardContent>
        </Card>
        <PwaCard />
      </div>
    </main>
  )
}

// PwaCard explains the installable web app and offers the native prompt
// when the browser has one. No server involved.
function PwaCard() {
  const { canInstall, installed, install } = usePwaInstall()
  return (
    <Card className="w-full">
      <CardHeader>
        <CardTitle className="text-base">Install the app</CardTitle>
        <CardDescription>
          Pocket Coder is a PWA. It runs in the browser but installs like a
          native app: home-screen icon, full-screen window, no app store.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2 text-sm text-muted-foreground">
        <ul className="flex list-disc flex-col gap-1 pl-5">
          <li>iPhone: Share, then Add to Home Screen.</li>
          <li>Android and desktop Chrome: use the Install button below.</li>
        </ul>
        {installed ? (
          <p className="text-sm">Installed. Open it from your home screen.</p>
        ) : canInstall ? (
          <Button variant="outline" size="sm" className="self-start" onClick={() => void install()}>
            Install Pocket Coder
          </Button>
        ) : (
          <p className="text-sm">
            No install prompt right now. On iPhone use Share. On desktop look
            for the install icon in the address bar.
          </p>
        )}
      </CardContent>
    </Card>
  )
}
