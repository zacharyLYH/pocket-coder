import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { api, errMsg, projectPath } from '@/lib/api'
import { isLaunchable, type Harness } from '@/lib/types'

// The launch timeout: CLI validation can take a while (freebuff downloads a platform binary on first run).
const LAUNCH_TIMEOUT_MS = 180_000

// New-tab dialog: every tab needs a user-supplied unique session name;
// harness dropdown lists only installed entries (install happens on the home page).
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
  const installedHarnesses = harnesses.filter((h) => isLaunchable(h) && h.installed)

  function reset() {
    setName('')
    setType('shell')
    setProgress('')
    setLaunchError(null)
  }

  // createShell creates a plain-shell session under the typed name.
  async function createShell(sessionName: string, signal: AbortSignal) {
    setProgress(`Launching ${sessionName}…`)
    await api(projectPath(projectId, '/sessions'), {
      method: 'POST',
      body: JSON.stringify({ name: sessionName, create: true }),
      signal,
    })
    return sessionName
  }

  // createHarness launches a harness session under the explicit name.
  async function createHarness(id: string, sessionName: string, signal: AbortSignal) {
    setProgress(`Launching ${sessionName}…`)
    const created = await api<{ name?: string }>(projectPath(projectId, '/sessions'), {
      method: 'POST',
      body: JSON.stringify({ harnessId: id, name: sessionName, create: true }),
      signal,
    })
    if (!created?.name) throw new Error('launch response missing session name')
    return created.name
  }

  async function launch(e: FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed) return
    setLaunching(true)
    setLaunchError(null)
    setProgress('Creating session…')
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), LAUNCH_TIMEOUT_MS)
    try {
      const isHarness = type !== 'shell'
      const attached = isHarness
        ? await createHarness(type, trimmed, controller.signal)
        : await createShell(trimmed, controller.signal)
      onOpenChange(false)
      reset()
      onLaunched(attached)
    } catch (err) {
      if (err instanceof DOMException && err.name === 'AbortError') {
        setLaunchError('Launch timed out after 3 minutes. Try again in a moment.')
      } else {
        setLaunchError(errMsg(err))
      }
      setProgress('')
    } finally {
      clearTimeout(timer)
      setLaunching(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={(o) => { if (!o) reset(); onOpenChange(o) }}>
      <DialogContent className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>New Tab</DialogTitle>
        </DialogHeader>
        <form onSubmit={launch} className="flex flex-col gap-3">
          <Input
            placeholder="Tab name (e.g. dev, debug, main)"
            value={name}
            onChange={(e) => setName(e.target.value)}
            autoFocus
            required
            disabled={launching}
          />
          <div className="flex flex-col gap-1.5">
            <label className="text-muted-foreground text-xs font-medium">Run</label>
            <select
              className="h-9 rounded-md border border-input bg-transparent px-3 text-sm shadow-xs outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              value={type}
              onChange={(e) => setType(e.target.value)}
              disabled={launching}
            >
              <option value="shell">Shell (bash)</option>
              {installedHarnesses.map((h) => (
                <option key={h.id} value={h.id}>
                  {h.name}
                </option>
              ))}
            </select>
          </div>
          {harness && (
            <p className="text-muted-foreground text-xs">
              Command: <span className="font-mono">{harness.command}</span>
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
            <Button type="submit" disabled={launching || !name.trim()}>
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
