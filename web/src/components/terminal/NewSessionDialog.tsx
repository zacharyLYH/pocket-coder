import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg } from '@/lib/api'
import type { Harness } from '@/lib/types'

// The launch timeout: harness installs (npm/pip) + CLI validation can
// take a while. 3 minutes covers slow networks and large packages.
const LAUNCH_TIMEOUT_MS = 180_000

// New-session dialog: a typed name for plain shells, or a harness from the
// registry (auto-named <harnessID>-<n> by the server). Harness launches may
// install on first use, so they run against a generous timeout with live
// progress text. Fields reset whenever the dialog closes.
export function NewSessionDialog({ open, onOpenChange, projectId, harnesses, onLaunched }: {
  open: boolean
  onOpenChange: (open: boolean) => void
  projectId: string
  harnesses: Harness[]
  onLaunched: (name: string) => void
}) {
  const [name, setName] = useState('')
  const [type, setType] = useState('shell') // 'shell' | harness id
  const [launching, setLaunching] = useState(false)
  const [progress, setProgress] = useState('')
  const [launchError, setLaunchError] = useState<string | null>(null)

  const harness = harnesses.find((h) => h.id === type)

  function reset() {
    setName('')
    setType('shell')
    setProgress('')
    setLaunchError(null)
  }

  async function launch(e: FormEvent) {
    e.preventDefault()
    const isHarness = type !== 'shell'
    // Harness sessions are auto-named by the server (<harnessID>-<n>); a
    // typed name only applies to plain shells. Using the typed name here
    // would create a stray bash session alongside the harness one.
    if (!isHarness && !name.trim()) return
    setLaunching(true)
    setLaunchError(null)
    setProgress('Creating session…')
    let attached = name.trim()
    try {
      let body: string
      if (isHarness) {
        // Harness launch — may be slow (install + validation)
        setProgress('Installing harness (this may take a minute)…')
        body = JSON.stringify({ harnessId: type })
      } else {
        setProgress('Starting shell session…')
        body = JSON.stringify({ name: attached })
      }
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(), LAUNCH_TIMEOUT_MS)
      try {
        const created = await api<{ name?: string }>(`/api/projects/${projectId}/sessions`, {
          method: 'POST',
          body,
          signal: controller.signal,
        })
        if (isHarness) {
          // the server picks the <harnessID>-<n> name to attach
          if (!created?.name) throw new Error('launch response missing session name')
          attached = created.name
        }
      } finally {
        clearTimeout(timer)
      }
      onOpenChange(false)
      reset()
      onLaunched(attached)
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        setLaunchError('Launch timed out after 3 minutes — the harness may still be installing. Try again in a moment.')
      } else {
        setLaunchError(errMsg(err))
      }
      setProgress('')
    } finally {
      setLaunching(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) reset(); onOpenChange(o) }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New Session</DialogTitle>
        </DialogHeader>
        <form onSubmit={launch} className="flex flex-col gap-3">
          {!harness ? (
            <Input
              placeholder="Session name (e.g. dev, debug, main)"
              value={name}
              onChange={(e) => setName(e.target.value)}
              autoFocus
              required
              disabled={launching}
            />
          ) : (
            <p className="text-muted-foreground text-xs">
              Sessions run by a harness are named automatically
              (<span className="font-mono">{harness.id}-1</span>, …).
            </p>
          )}
          <div className="flex flex-col gap-1.5">
            <label className="text-muted-foreground text-xs font-medium">Run</label>
            <select
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              value={type}
              onChange={(e) => setType(e.target.value)}
              disabled={launching}
            >
              <option value="shell">Shell (bash)</option>
              {harnesses.filter((h) => h.command !== 'bash').map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}{h.installed ? '' : ' — not installed in this project'}
                </option>
              ))}
            </select>
          </div>
          {harness && (
            <p className="text-muted-foreground text-xs">
              Command: <span className="font-mono">{harness.command}</span>
            </p>
          )}
          {harness && !harness.installed && (
            <p className="text-xs text-amber-600 dark:text-amber-400">
              Not installed in this project yet — install it from the Harnesses card on the home page first.
            </p>
          )}
          {launching && progress && <LaunchProgress text={progress} />}
          {launchError && (
            <p className="max-h-24 overflow-auto rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">
              {launchError}
            </p>
          )}
          <DialogFooter>
            <Button type="button" variant="ghost" disabled={launching} onClick={() => onOpenChange(false)}>
              Cancel
            </Button>
            <Button type="submit" disabled={launching || (!harness && !name.trim())}>
              {launching ? 'Launching…' : 'Create & Attach'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function LaunchProgress({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-2 rounded-md bg-blue-50 px-3 py-2 text-xs text-blue-700 dark:bg-blue-950 dark:text-blue-300">
      <svg className="size-4 animate-spin" viewBox="0 0 24 24" fill="none">
        <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
        <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
      </svg>
      {text}
    </div>
  )
}
