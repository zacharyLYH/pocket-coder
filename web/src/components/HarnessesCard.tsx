import { useEffect, useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
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
export function HarnessesCard({ projects, onInstalled, onBusyChange }: { projects: Project[]; onInstalled?: () => void; onBusyChange?: (busy: boolean) => void }) {
  const [harnesses, setHarnesses] = useState<Harness[]>([])
  const [pickerFor, setPickerFor] = useState<string | null>(null) // harness id or 'command'
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [rowMsg, setRowMsg] = useState<Record<string, RowMsg | undefined>>({})
  const [commandInput, setCommandInput] = useState('')
  const [addOpen, setAddOpen] = useState(false)

  function load() {
    api<{ harnesses: Harness[] }>('/api/harnesses')
      .then((data) => setHarnesses(data.harnesses))
      .catch(() => {})
  }

  useEffect(() => { load() }, [])

  useEffect(() => { onBusyChange?.(busyKey !== null) }, [busyKey, onBusyChange])

  function isInstalled(projectId: string, harnessId: string) {
    const p = projects.find((x) => x.id === projectId)
    return !!p?.harnesses?.includes(harnessId)
  }

  function openPicker(key: string) {
    // default to every project selected except those already installed for this harness
    const all: Record<string, boolean> = {}
    for (const p of projects) {
      if (key !== 'command' && isInstalled(p.id, key)) continue
      all[p.id] = true
    }
    setPicked(all)
    setRowMsg((m) => ({ ...m, [key]: undefined }))
    setPickerFor((cur) => (cur === key ? null : key))
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
      if (key !== 'command') onInstalled?.()
    } catch (err) {
      setRowMsg((m) => ({ ...m, [key]: { kind: 'error', text: errMsg(err) } }))
    } finally {
      setBusyKey(null)
    }
  }

  async function runCommand() {
    if (!commandInput.trim()) return
    // only a confirmed picker selection may drive exec — never ambient `picked`,
    // which can be empty or stale once the picker is closed
    if (pickerFor !== 'command') return
    await applyToProjects('command', '/api/projects/exec', { command: commandInput.trim() })
  }

  const suggestions = harnesses.filter(isLaunchable)

  return (
    <Card className="mt-4">
      <CardHeader>
        <CardTitle className="text-base">Harnesses</CardTitle>
        <CardDescription>
          Agent CLIs your projects can run. Install downloads now — pick the projects, errors surface here.
          New projects are never touched until you choose them.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {suggestions.length === 0 && (
          <p className="text-muted-foreground text-sm">No harnesses yet.</p>
        )}
        {suggestions.map((h) => (
          <div key={h.id} className="flex flex-col">
            <div className="flex items-center justify-between gap-2 text-sm">
              <div className="min-w-0">
                <span>{h.name}</span>
                {/* block + truncate keeps long install commands from
                    stretching the card; the full text is on hover */}
                <div className="truncate font-mono text-xs text-muted-foreground" title={h.install ?? h.command}>
                  {h.install ?? h.command}
                </div>
              </div>
              {h.install ? (
                <Button
                  size="sm"
                  variant={pickerFor === h.id ? 'secondary' : 'outline'}
                  className="shrink-0"
                  disabled={busyKey !== null}
                  onClick={() => openPicker(h.id)}
                >
                  {busyKey === h.id ? 'Installing…' : 'Install…'}
                </Button>
              ) : (
                <span className="shrink-0 text-muted-foreground text-xs">no download needed</span>
              )}
            </div>
            {pickerFor === h.id && (
              <ProjectPicker
                projects={projects}
                picked={picked}
                onToggle={(id) => setPicked((p) => ({ ...p, [id]: !p[id] }))}
                busy={busyKey !== null}
                installed={Object.fromEntries(projects.map((p) => [p.id, isInstalled(p.id, h.id)]))}
                applyLabel={`Install in ${Object.values(picked).filter(Boolean).length} project(s)`}
                onApply={() => applyToProjects(h.id, `/api/harnesses/${h.id}/install`, {})}
                onCancel={() => setPickerFor(null)}
              />
            )}
            {rowMsg[h.id] && <RowResult msg={rowMsg[h.id]!} />}
          </div>
        ))}

        {/* Arbitrary commands: the same picker, any shell command — the place
            to upgrade CLIs or run one-off maintenance across projects. */}
        <div className="mt-2 border-t pt-3">
          <p className="text-xs font-medium">Run a command in your projects</p>
          <p className="text-muted-foreground text-xs">
            Upgrades, maintenance, one-offs — runs synchronously in the selected projects.
          </p>
          <form onSubmit={(e) => { e.preventDefault(); void runCommand() }} className="mt-2 flex flex-col gap-2">
            <Input
              type="text"
              placeholder="e.g. npm i -g opencode-ai@latest"
              value={commandInput}
              onChange={(e) => setCommandInput(e.target.value)}
            />
            {pickerFor === 'command' ? (
              <ProjectPicker
                projects={projects}
                picked={picked}
                onToggle={(id) => setPicked((p) => ({ ...p, [id]: !p[id] }))}
                busy={busyKey !== null}
                applyLabel={`Run in ${Object.values(picked).filter(Boolean).length} project(s)`}
                onApply={() => void runCommand()}
                onCancel={() => setPickerFor(null)}
              />
            ) : (
              <Button
                type="button"
                variant="outline"
                className="self-start"
                disabled={!commandInput.trim() || busyKey !== null || projects.length === 0}
                onClick={() => openPicker('command')}
              >
                Choose projects…
              </Button>
            )}
            {rowMsg.command && <RowResult msg={rowMsg.command} />}
          </form>
        </div>

        <AddHarnessDialog
          open={addOpen}
          onOpenChange={setAddOpen}
          onAdded={load}
        />
      </CardContent>
    </Card>
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
            <span>{p.name}</span>
            {isInstalled && <span className="ml-auto text-muted-foreground">Installed</span>}
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
// command) — it lands in the suggestions above and the "+ New Session"
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
