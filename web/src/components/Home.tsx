import { useEffect, useState } from 'react'
import { BusyOverlay } from '@/components/BusyOverlay'
import { ProjectsCard } from '@/components/ProjectsCard'
import { SetupRows } from '@/components/SetupRows'
import { ThemeToggle } from '@/components/ThemeToggle'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { api, errMsg } from '@/lib/api'
import type { ExecResult, GitConfigStatus, Project, SSHKey } from '@/lib/types'
import { useAiConfig } from '@/hooks/useAiConfig'
import { useProjects } from '@/hooks/useProjects'

// Home: projects first, connections as three status rows, power tools last.
// No stacked cards implying one combined flow.
export function Home({ email, onLogout, navigate }: {
  email: string; onLogout: () => void; navigate: (to: string) => void
}) {
  const { projects, loading, error, refresh } = useProjects()
  const [sshKeys, setSshKeys] = useState<SSHKey[]>([])
  const [harnessBusy, setHarnessBusy] = useState(false)
  const [gitConfigured, setGitConfigured] = useState(true)
  const { status: aiStatus, refresh: refreshAi } = useAiConfig()
  const loadSshKeys = () => api<{ keys: SSHKey[] }>('/api/ssh-keys').then((d) => setSshKeys(d.keys)).catch(() => {})
  const loadGit = () => api<GitConfigStatus>('/api/git/config').then((d) => setGitConfigured(d.configured)).catch(() => {})
  useEffect(() => { loadSshKeys() }, [])
  useEffect(() => { loadGit() }, [])
  useEffect(() => { void refreshAi() }, [refreshAi])
  const needsAttention = !gitConfigured || sshKeys.length === 0 || !aiStatus?.configured
  async function logout() {
    try { await api('/api/auth/logout', { method: 'POST' }) } catch { /* still sign out */ }
    onLogout()
  }
  return (
    <>
      <header className="sticky top-0 z-10 border-b border-border/60 bg-background/80 backdrop-blur-xl">
        <div className="mx-auto flex w-full max-w-md items-center gap-2 px-4 py-3">
          <h1 className="text-[17px] font-semibold tracking-tight">Pocket Coder</h1>
          <span className="flex-1" />
          <ThemeToggle />
          <Button variant="ghost" size="sm" onClick={logout} className="min-h-[44px]">Log out</Button>
        </div>
      </header>
      <main className="mx-auto flex w-full max-w-md flex-col gap-3 px-4 pb-16 pt-4">
        <span className="sr-only">Welcome, {email}</span>
        <p className="text-sm text-muted-foreground" aria-label={`Signed in as ${email}`}>
          {projects.length === 0 && !loading ? `Hey ${email}, clone your first repo.` : `${projects.length} project${projects.length === 1 ? '' : 's'}`}
        </p>
        <ProjectsCard projects={projects} loading={loading} error={error} refresh={refresh} sshKeyCount={sshKeys.length} gitConfigured={gitConfigured} navigate={navigate} />
        <Card className="gap-2 py-4">
          <CardHeader className="px-4">
            <div className="flex items-center gap-2">
              <CardTitle className="text-[17px] tracking-tight">Connections</CardTitle>
              {needsAttention
                ? <span className="rounded-full bg-primary px-2 py-0.5 text-xs text-primary-foreground">action needed</span>
                : <span className="text-sm font-normal text-muted-foreground">done</span>}
            </div>
          </CardHeader>
          <CardContent className="px-2">
            <SetupRows sshKeys={sshKeys} gitConfigured={gitConfigured} ai={aiStatus} onGit={loadGit} onKeys={loadSshKeys} />
          </CardContent>
        </Card>
        <AdvancedBox projects={projects} busy={harnessBusy} onBusy={setHarnessBusy} />
      </main>
      {harnessBusy && <BusyOverlay />}
    </>
  )
}

// The global run-anywhere command box, demoted: uncommon, synchronous,
// multi-project fan-out. Hidden until opened so nobody mistakes it for
// the normal way to run things.
function AdvancedBox({ projects, busy, onBusy }: { projects: Project[]; busy: boolean; onBusy: (b: boolean) => void }) {
  const [command, setCommand] = useState('')
  const [picked, setPicked] = useState<Record<string, boolean>>({})
  const [open, setOpen] = useState(false)
  const [result, setResult] = useState<string | null>(null)
  function toggle(id: string) { setPicked((p) => ({ ...p, [id]: !p[id] })) }
  async function run() {
    const ids = Object.entries(picked).filter(([, on]) => on).map(([id]) => id)
    if (!command.trim() || ids.length === 0) return
    onBusy(true); setResult(null)
    try {
      const d = await api<{ results?: ExecResult[] }>('/api/projects/exec', { method: 'POST', body: JSON.stringify({ projectIds: ids, command: command.trim() }) })
      const failed = (d.results ?? []).filter((r) => r.status === 'error')
      setResult(failed.length > 0 ? failed.map((f) => `${f.project}: ${f.detail}`).join(' · ') : `Ran in ${ids.length} project${ids.length === 1 ? '' : 's'}.`)
    } catch (e) { setResult(errMsg(e)) } finally { onBusy(false) }
  }
  return (
    <details className="rounded-xl border bg-card shadow-sm" onToggle={(e) => {
      const isOpen = (e.target as HTMLDetailsElement).open
      setOpen(isOpen)
      if (isOpen) setPicked(Object.fromEntries(projects.map((p) => [p.id, true])))
    }}>
      <summary className="min-h-[44px] cursor-pointer list-none px-4 py-3 text-[15px] font-medium text-muted-foreground active:opacity-70">Advanced: run a command everywhere</summary>
      {open && (
        <div className="flex flex-col gap-2 px-4 pb-4">
          <p className="text-xs text-muted-foreground">Rarely needed. Most work happens inside a project's terminal. Runs now, synchronously, in each checked project.</p>
          <Input value={command} onChange={(e) => setCommand(e.target.value)} placeholder="e.g. npm i -g opencode-ai@latest" className="min-h-[44px] font-mono" />
          {projects.map((p) => (
            <label key={p.id} className="flex min-h-[44px] cursor-pointer items-center gap-2 text-sm active:opacity-70">
              <input type="checkbox" checked={!!picked[p.id]} onChange={() => toggle(p.id)} className="size-4" />
              <span className="truncate font-mono text-xs">{p.id}</span>
            </label>
          ))}
          {result && <p className="max-h-24 overflow-auto break-all text-xs text-muted-foreground">{result}</p>}
          <Button onClick={run} disabled={busy || !command.trim()} className="min-h-[44px]">{busy ? 'Running...' : 'Run in checked projects'}</Button>
        </div>
      )}
    </details>
  )
}
