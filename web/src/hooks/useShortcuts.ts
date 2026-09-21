import { useCallback, useEffect, useState } from 'react'
import { api, projectPath } from '@/lib/api'
import type { Shortcut } from '@/lib/shortcuts'

// The one server-backed shortcuts list: one fetch per mount plus an
// explicit reload, shared by the tab-strip modal and the home editor.
export function useShortcuts(projectId: string | null) {
  const [shortcuts, setShortcuts] = useState<Shortcut[]>([])
  const reload = useCallback(async () => {
    if (!projectId) return
    try {
      const d = await api<{ shortcuts?: Shortcut[] }>(projectPath(projectId))
      setShortcuts(d.shortcuts ?? [])
    } catch {
      // keep stale on transient failure
    }
  }, [projectId])
  useEffect(() => { reload() }, [reload])
  return { shortcuts, reload }
}
