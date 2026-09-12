import { useCallback, useEffect, useState } from 'react'

import { api } from '@/lib/api'

export type ProjectLogEntry = {
  id: number
  time: string
  type: string
  message: string
  data?: Record<string, unknown>
}

// Tails one project's running log (GET /api/projects/:id/logs, oldest
// first). Failures keep the last-known lines so a transient blip doesn't
// blank the tail mid-read.
export function useProjectLogs(projectId: string, limit = 200) {
  const [logs, setLogs] = useState<ProjectLogEntry[]>([])

  const refresh = useCallback(async () => {
    try {
      const d = await api<{ logs: ProjectLogEntry[] }>(`/api/projects/${projectId}/logs?limit=${limit}`)
      setLogs(d.logs ?? [])
    } catch {
      // keep last-known
    }
  }, [projectId, limit])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, 3000)
    return () => clearInterval(id)
  }, [refresh])

  return { logs, refresh }
}
