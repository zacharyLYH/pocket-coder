import { useCallback, useEffect, useState } from 'react'

import { api } from '@/lib/api'

// Shared fetch logic for a project's quick commands: one fetch per mount
// plus an explicit reload (after saves), used by TerminalHeader's inject
// menu and QuickCommandsModal. Each mount fetches independently so the modal
// sees fresh edits — "single source" means one hook, not one request.
export function useQuickCommands(projectId: string | null) {
  const [commands, setCommands] = useState<Record<string, string>>({})

  const reload = useCallback(async () => {
    if (!projectId) return
    try {
      const d = await api<{ quickCommands?: Record<string, string> }>(`/api/projects/${projectId}`)
      setCommands(d.quickCommands ?? {})
    } catch {
      // Keep stale on transient failure (same rationale as the ports list).
    }
  }, [projectId])

  useEffect(() => {
    reload()
  }, [reload])

  return { commands, reload }
}
