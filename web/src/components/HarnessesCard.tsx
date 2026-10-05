import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg, projectPath } from '@/lib/api'
import { isLaunchable } from '@/lib/types'
import type { ExecResult, Harness, Project } from '@/lib/types'

type RowMsg = { kind: 'ok' | 'error'; text: string }

// Harnesses card: the agent-CLI catalog for ONE project — this dialog always
// opens from a single project's menu, so Install/Update applies straight to
// that project. No project picker: harnesses are a per-project property, and
// opening this dialog for a project means you want the harness in THIS
// project. Update checks probe this project's container only.
export function HarnessesCard({ project, onInstalled, onBusyChange }: { project: Project; onInstalled?: () => void; onBusyChange?: (busy: boolean) => void }) {
  const [harnesses, setHarnesses] = useState<Harness[]>([])
  const [liveInstalled, setLiveInstalled] = useState<Record<string, boolean>>({})
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [rowMsg, setRowMsg] = useState<Record<string, RowMsg | undefined>>({})
  const [addOpen, setAddOpen] = useState(false)
  const [updates, setUpdates] = useState<Record<string, { current: string; latest: string }>>({})

  // Installed is true when EITHER source says so: the project's recorded
  // list (state.json desired state) or the live container probe. Binaries
  // can exist without a record — manual installs in the terminal, binaries
  // surviving on a reused home volume — and the probe is the only ground
  // truth for those, so it must be able to mark a row Installed on its own.
  function isInstalled(harnessId: string) {
    return (project.harnesses ?? []).includes(harnessId) || !!liveInstalled[harnessId]
  }

  const load = useCallback(() => {
    api<{ harnesses: Harness[] }>('/api/harnesses')
      .then((data) => {
        setHarnesses(data.harnesses)
      })
      .catch(() => {})
    // One round trip for the whole registry's live state in THIS project.
    // A stopped project (or any probe failure) leaves the map empty and the
    // recorded list alone — never hide an Installed row on a probe error.
    api<{ harnesses: { id: string; installed: boolean }[] }>(projectPath(project.id, '/harnesses'))
      .then((data) => {
        const next: Record<string, boolean> = {}
        for (const h of data.harnesses) {
          if (h.installed) next[h.id] = true
        }
        setLiveInstalled(next)
      })
      .catch(() => {})
  }, [project.id])

  // Update checks run against this project's container only: one probe per
  // harness, all in parallel. The map is rebuilt whole each run so cleared
  // conditions drop their badge. Anything unavailable (no npm package,
  // project stopped) stays silent.
  async function checkUpdates(list: Harness[]) {
    const settled = await Promise.allSettled(list.map(async (h) => {
      if (!h.install || !isInstalled(h.id)) return null
      const d = await api<{ current?: string; latest?: string; updateAvailable?: boolean }>(
        `/api/harnesses/${h.id}/update-check`,
        { method: 'POST', body: JSON.stringify({ projectIds: [project.id] }) },
      )
      return d.updateAvailable && d.current && d.latest ? { id: h.id, current: d.current, latest: d.latest } : null
    }))
    const fresh: Record<string, { current: string; latest: string }> = {}
    for (const r of settled) {
      if (r.status === 'fulfilled' && r.value) fresh[r.value.id] = { current: r.value.current, latest: r.value.latest }
    }
    setUpdates(fresh)
  }

  // One fetch per mount/project — not per render. (The previous version
  // had no dependency array here, so every setState re-render re-fired the
  // update checks and doubled the install-probe traffic.)
  useEffect(() => { load() }, [load])

  const checkedKey = useRef('')
  useEffect(() => {
    if (harnesses.length === 0) return
    const key = harnesses.map((h) => `${h.id}:${isInstalled(h.id)}`).join(',') + `|${project.id}`
    if (checkedKey.current === key) return
    checkedKey.current = key
    void checkUpdates(harnesses)
  }, [harnesses, project.id, project.harnesses, liveInstalled])

  useEffect(() => { onBusyChange?.(busyKey !== null) }, [busyKey, onBusyChange])

  // Install/update applies straight to this project — the dialog only ever
  // opens from one project's menu, so there is nothing to pick. The button
  // disables while the round-trip is in flight; the card re-reads which
  // harnesses this project has afterwards so Installed shows immediately.
  async function applyHarness(harnessId: string) {
    setBusyKey(harnessId)
    setRowMsg((m) => ({ ...m, [harnessId]: undefined }))
    try {
      const data = await api<{ results?: ExecResult[] }>(`/api/harnesses/${harnessId}/install`, {
        method: 'POST',
        body: JSON.stringify({ projectIds: [project.id] }),
      })
      const r = (data.results ?? [])[0]
      const msg: RowMsg | undefined = r?.status === 'ok'
        ? { kind: 'ok', text: 'Installed.' }
        : { kind: 'error', text: r ? `${r.project}: ${r.detail ?? r.status}` : 'install failed' }
      setRowMsg((m) => ({ ...m, [harnessId]: msg }))
      if (r?.status === 'ok') {
        // Drop the update badge; flip the row to "Installed" immediately
        // from live truth (the parent refresh re-reads the recorded list,
        // which converges via the same install). Either source shows
        // Installed, so the row is correct during the gap too.
        setLiveInstalled((m) => ({ ...m, [harnessId]: true }))
        setUpdates((u) => {
          if (!(harnessId in u)) return u
          const next = { ...u }
          delete next[harnessId]
          return next
        })
        onInstalled?.()
      }
    } catch (err) {
      setRowMsg((m) => ({ ...m, [harnessId]: { kind: 'error', text: errMsg(err) } }))
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
          // This dialog is scoped to one project: installed here → nothing
          // left to do; otherwise Install/Update hits this project directly.
          const installed = isInstalled(h.id)
          const busy = busyKey === h.id
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
              ) : installed ? (
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
                      onClick={() => void applyHarness(h.id)}
                      data-testid={`harness-update-${h.id}`}
                    >
                      {busy ? 'Updating…' : 'Update'}
                    </Button>
                  </span>
                ) : (
                  <span className="shrink-0 text-muted-foreground text-xs" data-testid={`harness-installed-${h.id}`}>Installed</span>
                )
              ) : (
                <Button
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  disabled={busyKey !== null}
                  onClick={() => void applyHarness(h.id)}
                  data-testid={`harness-install-${h.id}`}
                >
                  {busy ? 'Installing…' : 'Install'}
                </Button>
              )}
            </div>
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

// AddHarnessDialog registers a new harness (name, command, optional install
// command). Registering NEVER downloads anything: the binary is only
// fetched when the user explicitly installs it into chosen projects.
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
              placeholder="Startup command (e.g. my-agent)"
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
