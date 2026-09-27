import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { api, errMsg } from '@/lib/api'
import type { ButlerConfirm as Card } from '@/lib/types'

// ConfirmCard renders one pending write: old/new summary plus the blast
// radius, with Confirm and Discard. propose_env_fix adds a masked value
// field; the value travels only in the apply body, never in chat.
export function ButlerConfirm({ card, onDone }: { card: Card; onDone: (result: string | null) => void }) {
  const [value, setValue] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const needsValue = card.tool === 'propose_env_fix'

  async function act(path: string, body?: unknown) {
    setBusy(true)
    setError(null)
    try {
      const d = await api<{ result?: string }>(`/api/butler/confirms/${encodeURIComponent(card.id)}/${path}`, {
        method: 'POST',
        body: body ? JSON.stringify(body) : '{}',
      })
      onDone(d.result ?? null)
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-2xl border border-amber-500/40 bg-amber-500/10 px-4 py-3" data-testid="butler-confirm">
      <p className="text-sm font-medium" data-testid="butler-confirm-summary">{card.summary}</p>
      <p className="mt-1 text-xs text-muted-foreground" data-testid="butler-confirm-blast">{card.blastRadius}</p>
      {needsValue && (
        <input
          type="password"
          placeholder="Value (masked, never shown in chat)"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          disabled={busy}
          data-testid="butler-confirm-value"
          className="mt-2 w-full rounded-xl border bg-background px-3 py-1.5 text-sm"
        />
      )}
      {error && (
        <p className="mt-1 text-xs text-destructive" data-testid="butler-confirm-error">{error}</p>
      )}
      <div className="mt-2 flex gap-2">
        <Button
          size="sm"
          disabled={busy || (needsValue && !value.trim())}
          onClick={() => void act('apply', needsValue ? { value } : undefined)}
          data-testid="butler-confirm-ok"
        >
          Confirm
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={() => void act('discard')} data-testid="butler-confirm-no">
          Discard
        </Button>
      </div>
    </div>
  )
}
