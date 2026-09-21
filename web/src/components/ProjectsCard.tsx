import { useState, type FormEvent } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { api, errMsg } from '@/lib/api'
import { terminalPath } from '@/lib/paths'
import type { Project } from '@/lib/types'
import { ProjectMenu } from '@/components/ProjectMenu'

// Projects: the row itself opens the terminal. Everything else lives
// in the per-project menu; the clone form hides in a disclosure.
export function ProjectsCard({ projects, loading, error, refresh, sshKeyCount, gitConfigured = true, navigate }: {
  projects: Project[]; loading: boolean; error: string | null; refresh: () => Promise<void>
  sshKeyCount: number; gitConfigured?: boolean; navigate: (to: string) => void
}) {
  const [repoUrl, setRepoUrl] = useState('')
  const [branch, setBranch] = useState('')
  const [cloneMethod, setCloneMethod] = useState<'http' | 'ssh'>('http')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  async function createProject(e: FormEvent) {
    e.preventDefault()
    setCreating(true); setCreateError(null)
    try {
      await api('/api/projects', { method: 'POST', body: JSON.stringify({ repoUrl: repoUrl.trim(), branch: branch.trim(), cloneMethod }) })
      setRepoUrl(''); setBranch(''); await refresh()
    } catch (err) { setCreateError(errMsg(err)) } finally { setCreating(false) }
  }
  return (
    <Card className="gap-3 py-4">
      <CardHeader className="px-4">
        <CardTitle className="text-[17px] tracking-tight">Projects</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-1 px-2">
        {loading && <p className="px-2 text-sm text-muted-foreground">Loading projects…</p>}
        {!loading && error && <p className="px-2 text-sm text-destructive">Failed to load projects: {error}</p>}
        {!loading && !error && projects.length === 0 && (
          <p className="px-2 text-sm text-muted-foreground">No projects yet. Clone a repo to open a terminal anywhere.</p>
        )}
        {projects.map((p) => (
          <div key={p.id} data-testid={`project-card-${p.id}`} className="flex items-center gap-1 rounded-xl px-2 py-1 active:bg-muted">
            <button onClick={() => navigate(terminalPath(p.id, 'main'))} className="min-h-[44px] min-w-0 flex-1 cursor-pointer truncate text-left text-[15px]" aria-label={`Open ${p.id} terminal`}>
              <span className="block truncate font-medium underline-offset-4 hover:underline">{p.id.split('/')[1] ?? p.id}</span>
              <span className="block truncate font-mono text-xs text-muted-foreground">{p.id}</span>
            </button>
            <ProjectMenu project={p} projects={projects} onChanged={() => { void refresh() }} navigate={navigate} />
          </div>
        ))}
        <details className="mx-2 mt-1 rounded-xl bg-muted/50" {...(projects.length === 0 ? { open: true } : {})}>
          <summary className="min-h-[44px] cursor-pointer list-none px-3 py-3 text-sm font-medium active:opacity-70">Clone a repo</summary>
          <form onSubmit={createProject} className="flex flex-col gap-2 p-3 pt-0">
            <Input type="text" placeholder="GitHub Repo URL (e.g. https://github.com/owner/repo)" value={repoUrl} onChange={(e) => setRepoUrl(e.target.value)} className="min-h-[44px]" />
            <Input type="text" placeholder="Branch (optional)" value={branch} onChange={(e) => setBranch(e.target.value)} className="min-h-[44px]" />
            <div className="flex items-center gap-2 text-sm">
              <label className="shrink-0 whitespace-nowrap text-muted-foreground">Clone via:</label>
              <div className="flex shrink-0 rounded-md border text-xs" role="group" aria-label="Clone method">
                <button type="button" aria-pressed={cloneMethod === 'http'} onClick={() => setCloneMethod('http')} className={`min-h-[44px] px-4 ${cloneMethod === 'http' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}>HTTPS</button>
                <button type="button" aria-pressed={cloneMethod === 'ssh'} onClick={() => setCloneMethod('ssh')} className={`min-h-[44px] px-4 ${cloneMethod === 'ssh' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}>SSH</button>
              </div>
              {cloneMethod === 'ssh' && sshKeyCount === 0 && <span className="text-xs text-destructive">No SSH keys — add one under Connections below.</span>}
              {cloneMethod === 'ssh' && sshKeyCount > 0 && <span className="text-xs text-muted-foreground">{sshKeyCount} key(s) registered</span>}
            </div>
            {createError && <p className="max-h-24 overflow-auto break-all text-xs text-destructive">{createError}</p>}
            {!gitConfigured && <p className="text-xs text-muted-foreground" data-testid="git-setup-hint">Set up Git in Setup to create projects.</p>}
            <Button type="submit" disabled={creating || !repoUrl.trim() || !gitConfigured} className="min-h-[44px]">{creating ? 'Creating…' : 'Clone project'}</Button>
          </form>
        </details>
      </CardContent>
    </Card>
  )
}
