// Two independent connection rows. Git shows the server deploy key
// fingerprint (the page only renders once the key probes clean, so the
// row is never amber for connectivity); AI opens the model card.
import { useEffect, useState } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { GitCard } from '@/components/GitCard'
import { AICard } from '@/components/AICard'
import { api } from '@/lib/api'
import type { AIConfigStatus } from '@/lib/types'

export function SetupRows({ ai, onGit, onAi }: {
  ai: AIConfigStatus | null; onGit: () => void; onAi: () => void
}) {
  const [open, setOpen] = useState<'git' | 'ai' | null>(null)
  const [fingerprint, setFingerprint] = useState<string | null>(null)
  useEffect(() => {
    api<{ fingerprint: string }>('/api/ssh').then((d) => setFingerprint(d.fingerprint)).catch(() => {})
  }, [])
  const gitLabel = fingerprint ?? 'set'
  const aiLabel = ai?.configured ? (ai.model || 'set') : 'not set'
  return (
    <div className="flex flex-col gap-1" data-testid="setup-rows">
      <SetupRow label="Git" status={gitLabel} done testid="setup-git" onOpen={() => setOpen('git')} />
      <SetupRow label="AI" status={aiLabel} done={!!ai?.configured} testid="setup-ai" onOpen={() => setOpen('ai')} />
      <Dialog open={open === 'git'} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader><DialogTitle>Git</DialogTitle></DialogHeader>
          <GitCard onChanged={onGit} />
        </DialogContent>
      </Dialog>
      <Dialog open={open === 'ai'} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader><DialogTitle>AI</DialogTitle></DialogHeader>
          <AICard onChanged={onAi} />
        </DialogContent>
      </Dialog>
    </div>
  )
}

function SetupRow({ label, status, done, testid, onOpen }: {
  label: string; status: string; done: boolean; testid: string; onOpen: () => void
}) {
  return (
    <button onClick={onOpen} data-testid={testid} aria-label={`${label}, ${status}`} className="flex min-h-[44px] cursor-pointer items-center gap-2 rounded-xl px-2 py-1 text-left text-[15px] active:bg-muted">
      <span className={`size-2 shrink-0 rounded-full ${done ? 'bg-emerald-500' : 'bg-amber-500'}`} aria-hidden />
      <span className="font-medium">{label}</span>
      <span className="truncate text-sm text-muted-foreground">{status}</span>
      <span className="flex-1" />
      <span className="text-sm text-muted-foreground" aria-hidden>›</span>
    </button>
  )
}
