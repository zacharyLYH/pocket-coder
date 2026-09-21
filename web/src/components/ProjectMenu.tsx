import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { AlertDialog, AlertDialogAction, AlertDialogCancel, AlertDialogContent, AlertDialogDescription, AlertDialogFooter, AlertDialogHeader, AlertDialogTitle } from '@/components/ui/alert-dialog'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { HarnessesCard } from '@/components/HarnessesCard'
import { ShortcutsModal } from '@/components/shortcuts/ShortcutsModal'
import { api, projectPath } from '@/lib/api'
import { terminalPath } from '@/lib/paths'
import type { Project } from '@/lib/types'

// Per-project menu: shortcuts + harnesses live here, not on home.
// The row itself navigates; this menu holds the rest.
export function ProjectMenu({ project, projects, onChanged, navigate }: {
  project: Project; projects: Project[]; onChanged: () => void; navigate: (to: string) => void
}) {
  const [shortcutsOpen, setShortcutsOpen] = useState(false)
  const [harnessOpen, setHarnessOpen] = useState(false)
  const [harnessBusy, setHarnessBusy] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  async function confirmDelete() {
    setDeleteOpen(false)
    try { await api(projectPath(project.id, '?scope=all'), { method: 'DELETE' }); await onChanged() } catch { /* next refresh shows truth */ }
  }
  const installed = project.harnesses?.length ?? 0
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button size="sm" variant="ghost" data-testid={`project-menu-${project.id}`} aria-label={`More actions for ${project.id}`} className="min-h-[44px] min-w-[44px]">...</Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem onSelect={() => navigate(terminalPath(project.id, 'main'))}>Open terminal</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setShortcutsOpen(true)}>Shortcuts</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setHarnessOpen(true)}>Harnesses{installed > 0 ? ` (${installed})` : ''}</DropdownMenuItem>
          <DropdownMenuItem onSelect={() => setDeleteOpen(true)} className="text-destructive">Delete project</DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      {shortcutsOpen && <ShortcutsModal projectId={project.id} open={shortcutsOpen} onOpenChange={setShortcutsOpen} onSaved={onChanged} />}
      <Dialog open={harnessOpen} onOpenChange={setHarnessOpen}>
        <DialogContent className="max-h-[85dvh] overflow-y-auto sm:max-w-lg">
          <DialogHeader><DialogTitle>Harnesses for {project.id.split('/')[1] ?? project.id}</DialogTitle></DialogHeader>
          <HarnessesCard projects={projects} initialProjectId={project.id} onInstalled={onChanged} onBusyChange={setHarnessBusy} />
          {harnessBusy && <p className="text-sm text-muted-foreground">Working...</p>}
        </DialogContent>
      </Dialog>
      <AlertDialog open={deleteOpen} onOpenChange={setDeleteOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>Delete project</AlertDialogTitle>
            <AlertDialogDescription>This will permanently remove the project and all volumes. This action cannot be undone.</AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel>Cancel</AlertDialogCancel>
            <AlertDialogAction onClick={confirmDelete}>Delete</AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </>
  )
}
