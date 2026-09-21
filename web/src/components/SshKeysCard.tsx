import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errMsg } from '@/lib/api'
import type { SSHKey } from '@/lib/types'

// SSH keys card: register public keys so projects can clone
// private repos over SSH. The key list is owned by Home (the project form
// counts it); this card renders it and handles add/delete.
export function SshKeysCard({ keys, onChanged }: {
  keys: SSHKey[]
  onChanged: () => void
}) {
  const [keyInput, setKeyInput] = useState('')
  const [labelInput, setLabelInput] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function addKey(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api('/api/ssh-keys', {
        method: 'POST',
        body: JSON.stringify({ publicKey: keyInput.trim(), label: labelInput.trim() }),
      })
      setKeyInput('')
      setLabelInput('')
      onChanged()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  async function deleteKey(fp: string) {
    try {
      await api(`/api/ssh-keys/${fp}`, { method: 'DELETE' })
      onChanged()
    } catch {
      // retry on next load
    }
  }

  return (
    <div className="flex flex-col gap-2">
      {keys.length === 0 && <p className="text-sm text-muted-foreground">No keys registered.</p>}
      {keys.map((k) => (
        <div key={k.fingerprint} className="flex min-h-[44px] items-center justify-between gap-2 text-sm">
          <span className="truncate font-mono text-xs" title={k.publicKey}>{k.label || k.fingerprint}</span>
          <Button size="sm" variant="ghost" className="min-h-[44px] text-destructive" onClick={() => deleteKey(k.fingerprint)}>Delete</Button>
        </div>
      ))}
      <form onSubmit={addKey} className="flex flex-col gap-2 border-t pt-3">
        <Input type="text" placeholder="ssh-ed25519 AAAA... or ssh-rsa AAAA..." value={keyInput} onChange={(e) => setKeyInput(e.target.value)} required className="min-h-[44px]" />
        <Input type="text" placeholder="Label (optional, e.g. work-laptop)" value={labelInput} onChange={(e) => setLabelInput(e.target.value)} className="min-h-[44px]" />
        {error && <p className="text-destructive max-h-16 overflow-auto break-all text-xs">{error}</p>}
        <Button type="submit" disabled={busy || !keyInput.trim()} className="min-h-[44px]">{busy ? 'Adding...' : 'Add key'}</Button>
      </form>
    </div>
  )
}
