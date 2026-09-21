import { useState } from 'react'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { GitCard } from '@/components/GitCard'
import { SshKeysCard } from '@/components/SshKeysCard'
import { AICard } from '@/components/AICard'
import type { AIConfigStatus, SSHKey } from '@/lib/types'

// Three independent connection rows. Each shows its own status so a
// configured thing never looks missing, and each opens its own dialog
// so the rows never imply one combined flow. AI status is passed down
// from Home (single owner) so the row never renders stale.
export function SetupRows({ sshKeys, gitConfigured, ai, onGit, onKeys }: {
  sshKeys: SSHKey[]; gitConfigured: boolean; ai: AIConfigStatus | null; onGit: () => void; onKeys: () => void
}) {
  const [open, setOpen] = useState<'git' | 'ssh' | 'ai' | null>(null)
  const gitLabel = gitConfigured ? 'set' : 'not set'
  const sshLabel = sshKeys.length === 0 ? 'none' : `${sshKeys.length} key${sshKeys.length === 1 ? '' : 's'}`
  const aiLabel = ai?.configured ? (ai.model || 'set') : 'not set'
  return (
    <div className="flex flex-col gap-1" data-testid="setup-rows">
      <SetupRow label="Git" status={gitLabel} done={gitConfigured} testid="setup-git" onOpen={() => setOpen('git')} />
      <SetupRow label="SSH keys" status={sshLabel} done={sshKeys.length > 0} testid="setup-ssh" onOpen={() => setOpen('ssh')} />
      <SetupRow label="AI" status={aiLabel} done={!!ai?.configured} testid="setup-ai" onOpen={() => setOpen('ai')} />
      <Dialog open={open === 'git'} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader><DialogTitle>Git</DialogTitle></DialogHeader>
          <GitCard onChanged={onGit} />
        </DialogContent>
      </Dialog>
      <Dialog open={open === 'ssh'} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader><DialogTitle>SSH keys</DialogTitle></DialogHeader>
          <SshKeysCard keys={sshKeys} onChanged={onKeys} />
        </DialogContent>
      </Dialog>
      <Dialog open={open === 'ai'} onOpenChange={(o) => { if (!o) setOpen(null) }}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-md">
          <DialogHeader><DialogTitle>AI</DialogTitle></DialogHeader>
          <AICard />
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
