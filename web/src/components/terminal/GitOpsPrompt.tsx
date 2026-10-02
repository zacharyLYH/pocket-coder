import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { api, errMsg, projectPath } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'

// GitOpsPrompt: tailored AI instructions for the higher-level git ops this
// tab deliberately does not perform. The backend fills the template with
// live repo state (branch, upstream, dirty files); the user pastes the
// result into their terminal AI, which does the work.
type Op = 'pr' | 'sync' | 'undo'

const OPS: { id: Op; label: string; hint: string }[] = [
  { id: 'pr', label: 'Ship via PR', hint: 'Move work to a new branch, push, open a PR.' },
  { id: 'sync', label: 'Sync from base', hint: 'Update this branch from base (rebase or merge).' },
  { id: 'undo', label: 'Discard work', hint: 'Throw away local changes back to upstream.' },
]

export function GitOpsPrompt({ projectId, branch }: { projectId: string; branch: string | null }) {
  const [op, setOp] = useState<Op>('pr')
  const [newBranch, setNewBranch] = useState('')
  const [base, setBase] = useState('')
  const [prompt, setPrompt] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  async function get() {
    setBusy(true)
    setError(null)
    setCopied(false)
    try {
      const d = await api<{ prompt: string }>(projectPath(projectId, '/git/ops-prompt'), {
        method: 'POST',
        body: JSON.stringify({ op, newBranch: newBranch.trim(), base: base.trim() }),
      })
      setPrompt(d.prompt)
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  async function copy() {
    if (!prompt) return
    setCopied(await copyToClipboard(prompt))
  }

  const active = OPS.find((o) => o.id === op)!

  return (
    <div className="flex flex-col gap-2 rounded-lg border p-2" data-testid="git-ops">
      <div className="flex items-center gap-2">
        <span className="text-xs font-medium text-muted-foreground">Advanced git</span>
        <span className="flex-1" />
        <span className="text-[11px] text-muted-foreground">runs in your terminal AI, not here</span>
      </div>
      <p className="text-[11px] text-muted-foreground">
        This tab pushes straight to the current branch{branch ? ` (${branch})` : ''}. PRs, rebases/merges, and
        rollbacks run in your terminal AI — get a tailored prompt below and paste it there.
      </p>
      <div className="flex flex-wrap gap-2" role="group" aria-label="Git operation">
        {OPS.map((o) => (
          <Button
            key={o.id}
            size="sm"
            variant={op === o.id ? 'default' : 'outline'}
            className="min-h-[36px]"
            onClick={() => { setOp(o.id); setPrompt(null); setError(null) }}
            data-testid={`git-ops-${o.id}`}
          >
            {o.label}
          </Button>
        ))}
      </div>
      <p className="text-[11px] text-muted-foreground" data-testid="git-ops-hint">{active.hint}</p>
      {op === 'pr' && (
        <Input
          placeholder="New branch name (e.g. feat/login)"
          value={newBranch}
          onChange={(e) => setNewBranch(e.target.value)}
          aria-label="New branch name"
          data-testid="git-ops-new-branch"
        />
      )}
      {op !== 'undo' && (
        <Input
          placeholder="Base branch (blank = repo default)"
          value={base}
          onChange={(e) => setBase(e.target.value)}
          aria-label="Base branch"
          data-testid="git-ops-base"
        />
      )}
      <Button
        size="sm"
        variant="outline"
        className="min-h-[44px] self-start"
        disabled={busy || (op === 'pr' && !newBranch.trim())}
        onClick={() => void get()}
        data-testid="git-ops-get"
      >
        {busy ? 'Building…' : 'Get AI instructions'}
      </Button>
      {error && <p className="text-xs text-destructive" data-testid="git-ops-error">{error}</p>}
      {prompt && (
        <div className="flex flex-col gap-1.5 rounded-md border bg-muted/20 p-2">
          <pre className="text-xs whitespace-pre-wrap break-all text-muted-foreground" data-testid="git-ops-prompt">{prompt}</pre>
          <Button size="sm" variant="outline" className="min-h-[36px] self-start" onClick={() => void copy()} data-testid="git-ops-copy">
            {copied ? 'Copied ✓' : 'Copy instructions'}
          </Button>
        </div>
      )}
    </div>
  )
}
