import { useEffect, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg } from '@/lib/api'

type Row = { alias: string; command: string }

export function QuickCommandsModal({ projectId, open, onOpenChange, onSaved }: {
  projectId: string
  open: boolean
  onOpenChange: (open: boolean) => void
  onSaved?: () => void
}) {
  const [rows, setRows] = useState<Row[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    setError(null)
    api<{ quickCommands?: Record<string, string> }>(`/api/projects/${projectId}`)
      .then((d) => {
        const qc = d.quickCommands ?? {}
        setRows(Object.entries(qc).map(([alias, command]) => ({ alias, command })))
      })
      .catch((e) => setError(errMsg(e)))
  }, [open, projectId])

  async function save() {
    setBusy(true)
    setError(null)
    try {
      const map: Record<string, string> = {}
      for (const r of rows) {
        const a = r.alias.trim()
        const c = r.command.trim()
        if (!a || !c) throw new Error('alias and command must be non-empty')
        if (map[a]) throw new Error(`duplicate alias: ${a}`)
        if (!/^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$/.test(a)) throw new Error(`invalid alias: ${a}`)
        map[a] = c
      }
      await api(`/api/projects/${projectId}`, { method: 'PATCH', body: JSON.stringify({ quickCommands: map }) })
      onOpenChange(false)
      onSaved?.()
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader><DialogTitle>Quick commands</DialogTitle></DialogHeader>
        <div className="flex flex-col gap-2">
          {rows.length === 0 && <p className="text-sm text-muted-foreground">No quick commands yet.</p>}
          {rows.map((r, i) => (
            <div key={i} className="flex gap-2">
              <Input placeholder="alias" value={r.alias} onChange={(e) => setRows((prev) => prev.map((x, idx) => idx === i ? { ...x, alias: e.target.value } : x))} className="w-32" data-testid={`qc-alias-${i}`} />
              <Input placeholder="command" value={r.command} onChange={(e) => setRows((prev) => prev.map((x, idx) => idx === i ? { ...x, command: e.target.value } : x))} className="flex-1" data-testid={`qc-command-${i}`} />
              <Button variant="ghost" size="sm" onClick={() => setRows((prev) => prev.filter((_, idx) => idx !== i))} data-testid={`qc-delete-${i}`}>Delete</Button>
            </div>
          ))}
          <Button variant="outline" size="sm" onClick={() => setRows((prev) => [...prev, { alias: '', command: '' }])} data-testid="qc-add">Add</Button>
          {error && <p className="text-xs text-destructive" data-testid="qc-error">{error}</p>}
        </div>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>Cancel</Button>
          <Button onClick={save} disabled={busy} data-testid="qc-save">{busy ? 'Saving…' : 'Save'}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
