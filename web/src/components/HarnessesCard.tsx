import { useEffect, useRef, useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg } from '@/lib/api'
import { isLaunchable } from '@/lib/types'
import type { ExecResult, Harness, Project } from '@/lib/types'

type RowMsg = { kind: 'ok' | 'error'; text: string }

// Harnesses card: the agent-CLI catalog plus the command box — the one place
// where users orchestrate what runs in which projects. Everything is
// explicit and synchronous: pick the projects, the work happens now, and
// per-project results (or errors) surface right here. New projects are
// never auto-injected — they appear in the pickers and the user decides.
export function HarnessesCard({ projects, initialProjectId, onInstalled, onBusyChange }: { projects: Project[]; initialProjectId?: string; onInstalled?: () => void; onBusyChange?: (busy: boolean) => void }) {
  const [harnesses, setHarnesses] = useState<Harness[]>([])
  const [pickerFor, setPickerFor] = useState<string | null>(null) // harness id or 'command'
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [rowMsg, setRowMsg] = useState<Record<string, RowMsg | undefined>>({})
  const [addOpen, setAddOpen] = useState(false)
  const [updates, setUpdates] = useState<Record<string, { current: string; latest: string }>>({})
  const [pickerMode, setPickerMode] = useState<'install' | 'update'>('install')

  function installedIn(harnessId: string) {
    return projects.filter((p) => isInstalled(p.id, harnessId)).map((p) => p.id)
  }

  function load() {
    api<{ harnesses: Harness[] }>('/api/harnesses')
      .then((data) => {
        setHarnesses(data.harnesses)
      })
      .catch(() => {})
  }

  // Update checks run against installed harnesses only: one probe per
  // harness inside a running container, all in parallel. The map is rebuilt
  // whole each run so cleared conditions drop their badge. Anything
  // unavailable (no npm package, nothing running) stays silent.
  async function checkUpdates(list: Harness[]) {
    const settled = await Promise.allSettled(list.map(async (h) => {
      if (!h.install) return null
      const ids = installedIn(h.id)
      if (ids.length === 0) return null
      const d = await api<{ current?: string; latest?: string; updateAvailable?: boolean }>(
        `/api/harnesses/${h.id}/update-check`,
        { method: 'POST', body: JSON.stringify({ projectIds: ids }) },
      )
      return d.updateAvailable && d.current && d.latest ? { id: h.id, current: d.current, latest: d.latest } : null
    }))
    const fresh: Record<string, { current: string; latest: string }> = {}
    for (const r of settled) {
      if (r.status === 'fulfilled' && r.value) fresh[r.value.id] = { current: r.value.current, latest: r.value.latest }
    }
    setUpdates(fresh)
  }

  useEffect(() => { load() }, [])

  const checkedKey = useRef('')
  useEffect(() => {
    if (harnesses.length === 0 || projects.length === 0) return
    const key = harnesses.map((h) => `${h.id}:${installedIn(h.id).join('+')}`).join(',')
    if (checkedKey.current === key) return
    checkedKey.current = key
    void checkUpdates(harnesses)
  })

  useEffect(() => { onBusyChange?.(busyKey !== null) }, [busyKey, onBusyChange])

  function isInstalled(projectId: string, harnessId: string) {
    const p = projects.find((x) => x.id === projectId)
    return !!p?.harnesses?.includes(harnessId)
  }

  function openPicker(key: string) {
    setPickerMode('install')
    // from a project menu the run is scoped to that project; the picker can widen it
    if (initialProjectId) {
      setPicked({ [initialProjectId]: true })
      setRowMsg((m) => ({ ...m, [key]: undefined }))
      setPickerFor((cur) => (cur === key ? null : key))
      return
    }
    // default to every project selected except those already installed
    const all: Record<string, boolean> = {}
    for (const p of projects) {
      if (isInstalled(p.id, key)) continue
      all[p.id] = true
    }
    setPicked(all)
    setRowMsg((m) => ({ ...m, [key]: undefined }))
    setPickerFor((cur) => (cur === key ? null : key))
  }

  // Update path: the check found a newer registry version, so the picker
  // opens with the installed projects checked — Update re-runs install.
  function openUpdatePicker(h: Harness) {
    setPickerMode('update')
    const ids = installedIn(h.id)
    setPicked(Object.fromEntries(ids.map((id) => [id, true])))
    setRowMsg((m) => ({ ...m, [h.id]: undefined }))
    setPickerFor(h.id)
  }

  function summarize(key: string, results: ExecResult[]) {
    const failed = results.filter((r) => r.status === 'error')
    const skipped = results.filter((r) => r.status === 'skipped')
    const okCount = results.filter((r) => r.status === 'ok').length
    let msg: RowMsg
    if (failed.length > 0) {
      msg = { kind: 'error', text: failed.map((f) => `${f.project}: ${f.detail}`).join(' · ') }
    } else if (results.length === 0) {
      msg = { kind: 'ok', text: 'Nothing to do — no projects selected.' }
    } else {
      const skipNote = skipped.length > 0 ? ` (skipped ${skipped.length} stopped)` : ''
      msg = { kind: 'ok', text: `Applied to ${okCount} project${okCount === 1 ? '' : 's'}${skipNote}.` }
    }
    setRowMsg((m) => ({ ...m, [key]: msg }))
  }

  async function applyToProjects(key: string, url: string, body: object) {
    const projectIds = Object.entries(picked).filter(([, on]) => on).map(([id]) => id)
    setBusyKey(key)
    try {
      const data = await api<{ results?: ExecResult[] }>(url, {
        method: 'POST',
        body: JSON.stringify({ projectIds, ...body }),
      })
      summarize(key, (data.results ?? []) as ExecResult[])
      setPickerFor(null)
      setUpdates((u) => {
        if (!(key in u)) return u
        const next = { ...u }
        delete next[key]
        return next
      })
      onInstalled?.()
    } catch (err) {
      setRowMsg((m) => ({ ...m, [key]: { kind: 'error', text: errMsg(err) } }))
    } finally {
      setBusyKey(null)
    }
  }

  const suggestions = harnesses.filter(isLaunchable)

  return (
    <div className="flex flex-col gap-2">
        {suggestions.length === 0 && (
          <p className="text-muted-foreground text-sm">No harnesses yet.</p>
        )}
        {suggestions.map((h) => {
          // Installed everywhere shown → nothing left to do: no button.
          // Otherwise the button stays so the remaining projects can be
          // covered (the picker marks the installed ones).
          const fullyInstalled = projects.length > 0 && projects.every((p) => isInstalled(p.id, h.id))
          return (
          <div key={h.id} className="flex flex-col">
            <div className="flex items-center justify-between gap-2 text-sm">
              <div className="min-w-0">
                <span>{h.name}</span>
                {/* wrap instead of truncate so long install commands fit
                    the width instead of clipping */}
                <div className="font-mono text-xs text-muted-foreground break-words" title={h.install ?? h.command}>
                  {h.install ?? h.command}
                </div>
              </div>
              {!h.install ? (
                <span className="shrink-0 text-muted-foreground text-xs">no download needed</span>
              ) : fullyInstalled ? (
                updates[h.id] ? (
                  <span className="flex shrink-0 items-center gap-2">
                    <span className="text-xs text-muted-foreground" data-testid={`harness-update-badge-${h.id}`}>
                      {updates[h.id].current} → {updates[h.id].latest}
                    </span>
                    <Button
                      size="sm"
                      variant="outline"
                      className="shrink-0"
                      disabled={busyKey !== null}
                      onClick={() => openUpdatePicker(h)}
                      data-testid={`harness-update-${h.id}`}
                    >
                      {busyKey === h.id ? 'Updating…' : 'Update'}
                    </Button>
                  </span>
                ) : (
                  <span className="shrink-0 text-muted-foreground text-xs">Installed</span>
                )
              ) : (
                <Button
                  size="sm"
                  variant={pickerFor === h.id ? 'secondary' : 'outline'}
                  className="shrink-0"
                  disabled={busyKey !== null}
                  onClick={() => openPicker(h.id)}
                >
                  {busyKey === h.id ? 'Installing…' : 'Install…'}
                </Button>
              )}
            </div>
            {pickerFor === h.id && (
              <ProjectPicker
                projects={projects}
                picked={picked}
                onToggle={(id) => setPicked((p) => ({ ...p, [id]: !p[id] }))}
                busy={busyKey !== null}
                installed={Object.fromEntries(projects.map((p) => [p.id, isInstalled(p.id, h.id)]))}
                applyLabel={`${pickerMode === 'update' ? 'Update' : 'Install'} in ${Object.values(picked).filter(Boolean).length} project(s)`}
                onApply={() => applyToProjects(h.id, `/api/harnesses/${h.id}/install`, {})}
                onCancel={() => setPickerFor(null)}
              />
            )}
            {rowMsg[h.id] && <RowResult msg={rowMsg[h.id]!} />}
          </div>
          )
        })}

        {/* Arbitrary commands live on the home page's global Run card;
            this dialog keeps installs only. */}
        <AddHarnessDialog
          open={addOpen}
          onOpenChange={setAddOpen}
          onAdded={load}
        />
    </div>
  )
}

function RowResult({ msg }: { msg: RowMsg }) {
  return (
    <p className={`max-h-24 overflow-auto break-all text-xs ${msg.kind === 'error' ? 'text-destructive' : 'text-muted-foreground'}`}>
      {msg.text}
    </p>
  )
}

// ProjectPicker is the multi-select used by harness installs and the
// command box: check the projects a run should touch, then apply. All
// projects start checked; unchecking is how a run gets scoped.
export function ProjectPicker({ projects, picked, onToggle, busy, applyLabel, onApply, onCancel, installed }: {
  projects: Project[]
  picked: Record<string, boolean>
  onToggle: (id: string) => void
  busy: boolean
  applyLabel: string
  onApply: () => void
  onCancel: () => void
  installed?: Record<string, boolean>
}) {
  const count = Object.values(picked).filter(Boolean).length
  return (
    <div className="mt-1 flex flex-col gap-1.5 rounded-md border bg-muted/40 p-2">
      {projects.map((p) => {
        const isInstalled = !!installed?.[p.id]
        return (
          <label key={p.id} className="flex cursor-pointer items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={!!picked[p.id]}
              onChange={() => onToggle(p.id)}
              disabled={busy || isInstalled}
              className="accent-primary"
            />
            <span title={p.id} className="min-w-0 flex-1 truncate">{p.id}</span>
            {isInstalled && <span className="ml-auto shrink-0 text-muted-foreground">Installed</span>}
          </label>
        )
      })}
      <div className="mt-1 flex gap-2">
        <Button type="button" size="sm" disabled={busy || count === 0} onClick={onApply}>
          {busy ? 'Working…' : applyLabel}
        </Button>
        <Button type="button" size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </div>
  )
}

// AddHarnessDialog registers a new harness (name, command, optional install
// command) — it lands in the suggestions above and the "+ New Tab"
// picker. Registering NEVER downloads anything: the binary is only fetched
// when the user explicitly installs it into chosen projects. Saving writes a
// plugin file server-side; errors surface in-dialog.
function AddHarnessDialog({ open, onOpenChange, onAdded }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onAdded: () => void
}) {
  const [name, setName] = useState('')
  const [command, setCommand] = useState('')
  const [install, setInstall] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function submit(e: FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError(null)
    try {
      await api('/api/harnesses', {
        method: 'POST',
        body: JSON.stringify({ name: name.trim(), command: command.trim(), install: install.trim() }),
      })
      setName('')
      setCommand('')
      setInstall('')
      onAdded()
      onOpenChange(false)
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="border-t pt-3">
      <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(true)}>
        Add harness
      </Button>
      <Dialog open={open} onOpenChange={onOpenChange}>
        <DialogContent className="sm:max-w-md">
          <DialogHeader>
            <DialogTitle>Add harness</DialogTitle>
            <DialogDescription>
              Adds it to your catalog only — nothing is downloaded until you install it
              into a project below.
            </DialogDescription>
          </DialogHeader>
          <form onSubmit={submit} className="flex flex-col gap-3">
            <Input
              type="text"
              placeholder="Name (e.g. My Agent)"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
            />
            <Input
              type="text"
              placeholder="Command (e.g. my-agent)"
              value={command}
              onChange={(e) => setCommand(e.target.value)}
              required
            />
            <Input
              type="text"
              placeholder="Install command (optional, e.g. npm i -g my-agent)"
              value={install}
              onChange={(e) => setInstall(e.target.value)}
            />
            {error && <p className="text-destructive max-h-16 overflow-auto break-all text-xs">{error}</p>}
            <DialogFooter>
              <Button type="button" variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" disabled={busy || !name.trim() || !command.trim()}>
                {busy ? 'Adding…' : 'Add harness'}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
    </div>
  )
}
