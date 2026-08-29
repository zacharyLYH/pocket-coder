import { useState, type FormEvent } from 'react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { api, errMsg } from '@/lib/api'
import { terminalPath } from '@/lib/paths'
import type { Project } from '@/lib/types'
// Projects card: the sandbox list plus the create form (repo URL, branch,
// clone method). Deleting and creating are explicit and confirmed.
export function ProjectsCard({ projects, loading, error, refresh, sshKeyCount, navigate }: {
  projects: Project[]
  loading: boolean
  error: string | null
  refresh: () => Promise<void>
  sshKeyCount: number
  navigate: (to: string) => void
}) {
  const [repoUrl, setRepoUrl] = useState('')
  const [branch, setBranch] = useState('')
  const [cloneMethod, setCloneMethod] = useState<'http' | 'ssh'>('http')
  const [creating, setCreating] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<string | null>(null)

  async function createProject(e: FormEvent) {
    e.preventDefault()
    setCreating(true)
    setCreateError(null)
    try {
      const body: Record<string, string> = {}
      if (repoUrl.trim()) body.repoUrl = repoUrl.trim()
      if (branch.trim()) body.branch = branch.trim()
      if (repoUrl.trim()) body.cloneMethod = cloneMethod
      await api('/api/projects', { method: 'POST', body: JSON.stringify(body) })
      setRepoUrl('')
      setBranch('')
      await refresh()
    } catch (err) {
      setCreateError(errMsg(err))
    } finally {
      setCreating(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    setDeleteTarget(null)
    try {
      await api(`/api/projects/${deleteTarget}?scope=all`, { method: 'DELETE' })
      await refresh()
    } catch {
      // leave the row in place; the next refresh shows the truth
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Projects</CardTitle>
        <CardDescription>One terminal per project, session "main".</CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-2">
        {loading && <p className="text-muted-foreground text-sm">Loading projects…</p>}
        {!loading && error && (
          <p className="text-destructive text-sm">Failed to load projects: {error}</p>
        )}
        {!loading && !error && projects.length === 0 && (
          <p className="text-muted-foreground text-sm">No projects yet.</p>
        )}
        {projects.map((p) => (
          <div key={p.id} className="flex items-center justify-between text-sm">
            <span>{p.name}</span>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" onClick={() => navigate(terminalPath(p.id, 'main'))}>
                Terminal
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setDeleteTarget(p.id)}>
                Delete
              </Button>
            </div>
          </div>
        ))}

        <AlertDialog open={deleteTarget !== null} onOpenChange={(open) => { if (!open) setDeleteTarget(null) }}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Delete project</AlertDialogTitle>
              <AlertDialogDescription>
                This will permanently remove the project, its container, and all volumes. This action cannot be undone.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Cancel</AlertDialogCancel>
              <AlertDialogAction onClick={confirmDelete}>Delete</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>

        <form onSubmit={createProject} className="mt-2 flex flex-col gap-2 border-t pt-3">
          <Input
            type="text"
            placeholder="Repo URL (optional — blank = plain sandbox)"
            value={repoUrl}
            onChange={(e) => setRepoUrl(e.target.value)}
          />
          <Input
            type="text"
            placeholder="Branch (optional)"
            value={branch}
            onChange={(e) => setBranch(e.target.value)}
          />
          {repoUrl.trim() && (
            <div className="flex items-center gap-2 text-sm">
              <label className="shrink-0 whitespace-nowrap text-muted-foreground">Clone via:</label>
              <div className="flex shrink-0 rounded-md border">
                <button
                  type="button"
                  className={`px-3 py-1 text-xs ${cloneMethod === 'http' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}
                  onClick={() => setCloneMethod('http')}
                >
                  HTTPS
                </button>
                <button
                  type="button"
                  className={`px-3 py-1 text-xs ${cloneMethod === 'ssh' ? 'bg-primary text-primary-foreground' : 'hover:bg-muted'}`}
                  onClick={() => setCloneMethod('ssh')}
                >
                  SSH
                </button>
              </div>
              {cloneMethod === 'ssh' && sshKeyCount === 0 && (
                <span className="text-destructive text-xs">No SSH keys — add one below.</span>
              )}
              {cloneMethod === 'ssh' && sshKeyCount > 0 && (
                <span className="text-muted-foreground text-xs">{sshKeyCount} key(s) registered</span>
              )}
            </div>
          )}
          {createError && <p className="text-destructive max-h-24 overflow-auto break-all text-xs">{createError}</p>}
          <Button type="submit" disabled={creating}>
            {creating ? 'Creating…' : 'Create project'}
          </Button>
        </form>
      </CardContent>
    </Card>
  )
}
