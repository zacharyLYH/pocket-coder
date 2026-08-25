import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { api, errMsg } from '@/lib/api'
import type { SSHKey } from '@/lib/types'

// SSH keys card: register public keys so project sandboxes can clone
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
      await fetch(`/api/ssh-keys/${fp}`, { method: 'DELETE' })
      onChanged()
    } catch {
      // retry on next load
    }
  }

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle className="text-base">SSH Keys</CardTitle>
        <CardDescription>Register public keys to clone private repos via SSH into your sandboxes.</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {keys.length === 0 && (
          <p className="text-muted-foreground text-sm">No keys registered.</p>
        )}
        {keys.map((k) => (
          <div key={k.fingerprint} className="flex items-center justify-between text-sm">
            <span className="truncate font-mono text-xs" title={k.publicKey}>
              {k.label || k.fingerprint}
            </span>
            <Button size="sm" variant="ghost" className="text-destructive" onClick={() => deleteKey(k.fingerprint)}>
              Delete
            </Button>
          </div>
        ))}
        <form onSubmit={addKey} className="mt-2 flex flex-col gap-2 border-t pt-3">
          <Input
            type="text"
            placeholder="ssh-ed25519 AAAA... or ssh-rsa AAAA..."
            value={keyInput}
            onChange={(e) => setKeyInput(e.target.value)}
            required
          />
          <Input
            type="text"
            placeholder="Label (optional, e.g. work-laptop)"
            value={labelInput}
            onChange={(e) => setLabelInput(e.target.value)}
          />
          {error && <p className="text-destructive max-h-16 overflow-auto break-all text-xs">{error}</p>}
          <Button type="submit" disabled={busy || !keyInput.trim()}>
            {busy ? 'Adding…' : 'Add key'}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
