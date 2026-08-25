import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'

// Email → PIN login against the real backend. The PIN arrives by email
// (or the server console in dev/e2e).
export function LoginForm({ onLoggedIn }: { onLoggedIn: (email: string) => void }) {
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
    <main className="bg-muted/40 grid min-h-dvh place-items-center p-4">
      <Card className="w-full max-w-sm">
        <CardHeader>
          <CardTitle>Side Project Saviour</CardTitle>
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
    </main>
  )
}
