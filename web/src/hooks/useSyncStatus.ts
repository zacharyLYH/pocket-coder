import { useEffect, useState } from 'react'

import { api, projectPath } from '@/lib/api'

export type SyncStatus = 'idle' | 'uploading' | 'downloading'

// useSyncStatus polls a project's live sync state. Work Sync is driven by
// the laptop, so the browser can only learn about a transfer by asking.
// While a status is live the UI blocks interaction with that project.
export function useSyncStatus(projectId: string | null | undefined, enabled = true): SyncStatus {
  const [status, setStatus] = useState<SyncStatus>('idle')
  useEffect(() => {
    if (!projectId || !enabled) {
      setStatus('idle')
      return
    }
    let alive = true
    const tick = async () => {
      try {
        const res = await api<{ status: SyncStatus }>(projectPath(projectId, '/sync'))
        if (alive) setStatus(res.status)
      } catch {
        // A transient poll failure must not unlock a project mid-transfer
        // by itself; the next tick corrects it.
      }
    }
    void tick()
    const id = setInterval(() => void tick(), 1000)
    return () => {
      alive = false
      clearInterval(id)
    }
  }, [projectId, enabled])
  return status
}
