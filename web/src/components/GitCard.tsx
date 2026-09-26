import { useEffect, useState } from 'react'
import { FlaskConical } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { api, errMsg, probeErr, probeSignal } from '@/lib/api'
import type { GitIdentity } from '@/lib/types'

// GitCard: the shared git identity list. Rows show an editable label
// plus test and delete inline; deletes confirm with the blast radius.
// Tokens never render.
//
// Save is gated on valid fields, not on tested: the server probes before
// writing, so one Save costs one provider call. Test is an optional
// pre-check, not a second toll.
export function GitCard({ onChanged }: { onChanged?: () => void }) {
  const [ids, setIds] = useState<GitIdentity[]>([])
  const [label, setLabel] = useState('')
  const [name, setName] = useState('')
  const [email, setEmail] = useState('')
  const [token, setToken] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [tested, setTested] = useState(false)
  const [testedId, setTestedId] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [deleteId, setDeleteId] = useState<string | null>(null)

  async function load() {
    try {
      const d = await api<{ identities: GitIdentity[] }>('/api/git/identities')
      setIds(d.identities ?? [])
    } catch { /* retry on next open */ }
  }
  useEffect(() => { void load() }, [])

  function markDirty() {
    setTested(false)
    setSaved(false)
  }

  const body = () => JSON.stringify({ label: label.trim(), name: name.trim(), email: email.trim(), token: token.trim() })
  const valid = name.trim() !== '' && email.trim() !== '' && token.trim() !== ''

  async function test() {
    setBusy('test')
    setError(null)
    setSaved(false)
    try {
      await api('/api/git/identities/test', { method: 'POST', body: body(), signal: probeSignal() })
      setTested(true)
    } catch (err) {
      setError(probeErr(err))
      setTested(false)
    } finally {
      setBusy(null)
    }
  }

  async function save() {
    setBusy('save')
    setError(null)
    try {
      await api('/api/git/identities', { method: 'POST', body: body(), signal: probeSignal() })
      setSaved(true)
      setLabel('')
      setName('')
      setEmail('')
      setToken('')
      setTested(false)
      await load()
      onChanged?.()
    } catch (err) {
      setError(probeErr(err))
    } finally {
      setBusy(null)
    }
  }

  async function retest(id: string) {
    setBusy(id)
    setError(null)
    try {
      await api(`/api/git/identities/${id}/test`, { method: 'POST', signal: probeSignal() })
      setTestedId(id)
    } catch (err) {
      setError(probeErr(err))
      setTestedId(null)
    } finally {
      setBusy(null)
    }
  }

  // Label-only edit resends the row with an empty token: the server keeps
  // the stored one and skips the probe, so a rename never burns quota.
  async function saveLabel(g: GitIdentity, next: string) {
    if (next === (g.label || '')) return
    setBusy(g.id)
    setError(null)
    try {
      await api(`/api/git/identities/${g.id}`, {
        method: 'PUT',
        body: JSON.stringify({ label: next, name: g.name, email: g.email, token: '' }),
        signal: probeSignal(),
      })
      await load()
      onChanged?.()
    } catch (err) {
      setError(probeErr(err))
    } finally {
      setBusy(null)
    }
  }

  async function remove(id: string) {
    setDeleteId(null)
    setBusy(id)
    try {
      await api(`/api/git/identities/${id}`, { method: 'DELETE' })
      if (testedId === id) setTestedId(null)
      await load()
      onChanged?.()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
    }
  }

  const condemned = ids.find((g) => g.id === deleteId)
  const condemnedName = condemned ? condemned.label || condemned.name : ''

  return (
    <div data-testid="git-card" className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">Shared identities + GitHub PATs for clone, push, pull. Test first, then save.{' '}
        <a href="https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens" target="_blank" rel="noreferrer" className="underline">What's a PAT?</a>
        {tested && <span> tested</span>}
        {saved && <span> saved</span>}
      </p>
      {ids.map((g) => (
        <div key={g.id} className="flex min-h-[44px] items-center justify-between gap-2 text-sm">
          <Input
            type="text"
            defaultValue={g.label}
            placeholder="Label"
            aria-label={`Label for ${g.name}`}
            className="min-h-[44px] max-w-28 font-mono text-xs"
            onBlur={(e) => { void saveLabel(g, e.target.value) }}
          />
          <span className="truncate text-xs text-muted-foreground" title={`${g.name} ${g.email}`}>{g.name}{testedId === g.id && ' tested'}</span>
          <span className="flex gap-1">
            <Button size="sm" variant="ghost" className="min-h-[44px]" disabled={busy !== null} onClick={() => void retest(g.id)}>
              <FlaskConical className="size-4" />{busy === g.id ? '...' : 'Test'}
            </Button>
            <Button size="sm" variant="ghost" className="min-h-[44px] text-destructive" disabled={busy !== null} onClick={() => setDeleteId(g.id)}>Delete</Button>
          </span>
        </div>
      ))}
      <Input type="text" placeholder="Label (e.g. work)" value={label} onChange={(e) => { setLabel(e.target.value); markDirty() }} data-testid="git-label" className="min-h-[44px]" />
      <Input type="text" placeholder="Name" value={name} onChange={(e) => { setName(e.target.value); markDirty() }} data-testid="git-name" className="min-h-[44px]" />
      <Input type="text" placeholder="Email" value={email} onChange={(e) => { setEmail(e.target.value); markDirty() }} data-testid="git-email" className="min-h-[44px]" />
      <Input type="password" placeholder="Personal Access Token (repo scope)" value={token} onChange={(e) => { setToken(e.target.value); markDirty() }} data-testid="git-token" className="min-h-[44px]" />
      {error && <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="git-error">{error}</p>}
      <div className="flex gap-2">
        <Button variant="outline" className="min-h-[44px] flex-1" disabled={busy !== null || !valid} onClick={() => void test()} data-testid="git-test">
          <FlaskConical className="size-4" />{busy === 'test' ? 'Testing...' : 'Test'}
        </Button>
        <Button className="min-h-[44px] flex-1" disabled={busy !== null || !valid} onClick={() => void save()} data-testid="git-save">
          {busy === 'save' ? 'Saving...' : 'Save'}
        </Button>
      </div>
      <AlertDialog open={deleteId !== null} onOpenChange={(o) => { if (!o) setDeleteId(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete identity</AlertDialogTitle>
            <AlertDialogDescription>
              Remove &quot;{condemnedName}&quot;?{ids.length <= 1 ? ' Project creation, push, and pull stop until another identity is saved.' : ''} This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { if (deleteId) void remove(deleteId) }} data-testid="git-confirm-delete">Delete</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
