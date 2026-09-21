import { useEffect, useState } from 'react'
import { FlaskConical } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errMsg } from '@/lib/api'

// GitCard: the one global git identity + HTTPS token (GitHub PAT).
// Test-then-save like AICard, prefilled when configured.
export function GitCard({ onChanged }: { onChanged?: () => void }) {
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [token, setToken] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [tested, setTested] = useState(false)
  const [busy, setBusy] = useState<'test' | 'save' | null>(null)
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    api<{ name: string; email: string; hasToken: boolean; configured: boolean }>('/api/git/config')
      .then((d) => { setName(d.name); setEmail(d.email) })
      .catch(() => {})
  }, [])

  function markDirty() {
    setTested(false)
    setSaved(false)
  }

  const body = () => JSON.stringify({ name: name.trim(), email: email.trim(), token: token.trim() })

  async function test() {
    setBusy('test')
    setError(null)
    setSaved(false)
    try {
      await api('/api/git/test', { method: 'POST', body: body() })
      setTested(true)
    } catch (err) {
      setError(errMsg(err))
      setTested(false)
    } finally {
      setBusy(null)
    }
  }

  async function save() {
    setBusy('save')
    setError(null)
    try {
      await api('/api/git/config', { method: 'POST', body: body() })
      setSaved(true)
      onChanged?.()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div data-testid="git-card" className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">One identity + GitHub PAT for clone, push, pull. Test first, then save.{' '}
        <a href="https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens" target="_blank" rel="noreferrer" className="underline">What's a PAT?</a>
        {tested && <span> tested</span>}
        {saved && <span> saved</span>}
      </p>
      <Input type="text" placeholder="Name" value={name} onChange={(e) => { setName(e.target.value); markDirty() }} data-testid="git-name" className="min-h-[44px]" />
      <Input type="text" placeholder="Email" value={email} onChange={(e) => { setEmail(e.target.value); markDirty() }} data-testid="git-email" className="min-h-[44px]" />
      <Input type="password" placeholder="Personal Access Token (repo scope)" value={token} onChange={(e) => { setToken(e.target.value); markDirty() }} data-testid="git-token" className="min-h-[44px]" />
      {error && <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="git-error">{error}</p>}
      <div className="flex gap-2">
        <Button variant="outline" className="min-h-[44px] flex-1" disabled={busy !== null || !name.trim() || !email.trim() || !token.trim()} onClick={() => void test()} data-testid="git-test">
          <FlaskConical className="size-4" />{busy === 'test' ? 'Testing...' : 'Test'}
        </Button>
        <Button className="min-h-[44px] flex-1" disabled={busy !== null || !tested} onClick={() => void save()} data-testid="git-save">
          {busy === 'save' ? 'Saving...' : 'Save'}
        </Button>
      </div>
    </div>
  )
}
