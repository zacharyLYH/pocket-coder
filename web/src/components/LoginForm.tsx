import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { usePwaInstall } from '@/hooks/usePwaInstall'

// Email + password login against the real backend: the one account per
// instance, set by setup.sh.
export function LoginForm({
  onLoggedIn,
  navigate,
}: {
  onLoggedIn: (email: string) => void
  navigate?: (to: string) => void
}) {
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const res = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email, password }),
      })
      if (!res.ok) throw new Error(`HTTP ${res.status}`)
      onLoggedIn(email)
    } catch {
      // One generic message for every failure: the server never says
      // which half was wrong, and neither do we.
      setError('Invalid email or password.')
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
            <CardDescription>Sign in with your email and password.</CardDescription>
          </CardHeader>
          <CardContent>
            <form onSubmit={submit} className="flex flex-col gap-3">
              <Input
                type="email"
                required
                autoComplete="email"
                placeholder="you@example.com"
                value={email}
                onChange={(e) => setEmail(e.target.value)}
              />
              <Input
                type="password"
                required
                autoComplete="current-password"
                placeholder="Password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
              {error && <p className="text-destructive text-sm">{error}</p>}
              <Button type="submit" disabled={busy}>
                Log in
              </Button>
            </form>
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
          <li>
            iPhone: Share, then Add to Home Screen.{' '}
            <a
              href="https://support.apple.com/en-my/guide/iphone/iphea86e5236/ios"
              target="_blank"
              rel="noopener noreferrer"
              className="text-primary underline underline-offset-4"
            >
              Apple guide
            </a>
            .
          </li>
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
