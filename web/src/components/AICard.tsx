import { useEffect, useState } from 'react'
import { FlaskConical } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { api, errMsg, probeErr, probeSignal } from '@/lib/api'
import type { AIModel } from '@/lib/types'

// AICard: the shared model list. Rows show an editable label plus test
// and delete inline; deletes confirm with the blast radius. Keys never
// render.
//
// Save is gated on valid fields, not on tested: the server probes before
// writing, so one Save costs one provider call. Test is an optional
// pre-check, not a second toll.
export function AICard({ onChanged }: { onChanged?: () => void }) {
  const [models, setModels] = useState<AIModel[]>([])
  const [label, setLabel] = useState('')
  const [baseURL, setBaseURL] = useState('')
  const [apiKey, setApiKey] = useState('')
  const [model, setModel] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [tested, setTested] = useState(false)
  const [testedId, setTestedId] = useState<string | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [deleteId, setDeleteId] = useState<string | null>(null)

  async function load() {
    try {
      const d = await api<{ models: AIModel[] }>('/api/ai/models')
      setModels(d.models ?? [])
    } catch { /* retry on next open */ }
  }
  useEffect(() => { void load() }, [])

  function markDirty() {
    setTested(false)
    setSaved(false)
  }

  const body = () => JSON.stringify({ label: label.trim(), baseURL: baseURL.trim(), apiKey, model: model.trim() })
  const valid = baseURL.trim() !== '' && apiKey !== '' && model.trim() !== ''

  async function test() {
    setBusy('test')
    setError(null)
    setSaved(false)
    try {
      await api('/api/ai/models/test', { method: 'POST', body: body(), signal: probeSignal() })
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
      await api('/api/ai/models', { method: 'POST', body: body(), signal: probeSignal() })
      setSaved(true)
      setLabel('')
      setBaseURL('')
      setApiKey('')
      setModel('')
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
      await api(`/api/ai/models/${id}/test`, { method: 'POST', signal: probeSignal() })
      setTestedId(id)
    } catch (err) {
      setError(probeErr(err))
      setTestedId(null)
    } finally {
      setBusy(null)
    }
  }

  // Label-only edit resends the row with an empty key: the server keeps
  // the stored one and skips the probe, so a rename never burns quota.
  async function saveLabel(m: AIModel, next: string) {
    if (next === (m.label || '')) return
    setBusy(m.id)
    setError(null)
    try {
      await api(`/api/ai/models/${m.id}`, {
        method: 'PUT',
        body: JSON.stringify({ label: next, baseURL: m.baseURL, model: m.model, apiKey: '' }),
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
      await api(`/api/ai/models/${id}`, { method: 'DELETE' })
      if (testedId === id) setTestedId(null)
      await load()
      onChanged?.()
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(null)
    }
  }

  const condemned = models.find((m) => m.id === deleteId)
  const condemnedName = condemned ? condemned.label || condemned.model : ''

  return (
    <div data-testid="ai-card" className="flex flex-col gap-2">
      <p className="text-xs text-muted-foreground">Shared model list for codemaps and the butler. Test first, then save.
        {tested && <span> tested</span>}
        {saved && <span> saved</span>}
      </p>
      {models.length === 0 && <p className="text-sm text-muted-foreground">No models yet. Codemaps stay disabled until one is saved.</p>}
      {models.map((m) => (
        <div key={m.id} className="flex min-h-[44px] items-center justify-between gap-2 text-sm">
          <Input
            type="text"
            defaultValue={m.label}
            placeholder="Label"
            aria-label={`Label for ${m.model}`}
            className="min-h-[44px] max-w-28 font-mono text-xs"
            onBlur={(e) => { void saveLabel(m, e.target.value) }}
          />
          <span className="truncate text-xs text-muted-foreground" title={`${m.baseURL} ${m.model}`}>{m.model}{testedId === m.id && ' tested'}</span>
          <span className="flex gap-1">
            <Button size="sm" variant="ghost" className="min-h-[44px]" disabled={busy !== null} onClick={() => void retest(m.id)}>
              <FlaskConical className="size-4" />{busy === m.id ? '...' : 'Test'}
            </Button>
            <Button size="sm" variant="ghost" className="min-h-[44px] text-destructive" disabled={busy !== null} onClick={() => setDeleteId(m.id)}>Delete</Button>
          </span>
        </div>
      ))}
      <Input type="text" placeholder="Label (e.g. cheap-mini)" value={label} onChange={(e) => { setLabel(e.target.value); markDirty() }} data-testid="ai-label" className="min-h-[44px]" />
      <Input type="text" placeholder="Base URL (https://api.openai.com/v1)" value={baseURL} onChange={(e) => { setBaseURL(e.target.value); markDirty() }} data-testid="ai-base-url" className="min-h-[44px]" />
      <Input type="password" placeholder="API key" value={apiKey} onChange={(e) => { setApiKey(e.target.value); markDirty() }} data-testid="ai-api-key" className="min-h-[44px]" />
      <Input type="text" placeholder="Model (gpt-4o)" value={model} onChange={(e) => { setModel(e.target.value); markDirty() }} data-testid="ai-model" className="min-h-[44px]" />
      {error && <p className="text-destructive max-h-24 overflow-auto break-all text-xs" data-testid="ai-error">{error}</p>}
      <div className="flex gap-2">
        <Button variant="outline" className="min-h-[44px] flex-1" disabled={busy !== null || !valid} onClick={() => void test()} data-testid="ai-test">
          <FlaskConical className="size-4" />{busy === 'test' ? 'Testing...' : 'Test'}
        </Button>
        <Button className="min-h-[44px] flex-1" disabled={busy !== null || !valid} onClick={() => void save()} data-testid="ai-save">
          {busy === 'save' ? 'Saving...' : 'Save'}
        </Button>
      </div>
      <AlertDialog open={deleteId !== null} onOpenChange={(o) => { if (!o) setDeleteId(null) }}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete model</AlertDialogTitle>
            <AlertDialogDescription>
              Remove &quot;{condemnedName}&quot;?{models.length <= 1 ? ' Codemaps and the butler stop until another model is saved.' : ''} This cannot be undone.
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={() => { if (deleteId) void remove(deleteId) }} data-testid="ai-confirm-delete">Delete</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
