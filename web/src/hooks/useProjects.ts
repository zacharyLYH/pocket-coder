import { useCallback, useEffect, useState } from 'react'

import { api, errMsg } from '@/lib/api'
import type { Project } from '@/lib/types'

export function useProjects() {
  const [projects, setProjects] = useState<Project[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const data = await api<{ projects: Project[] }>('/api/projects')
      setProjects(data.projects)
      setError(null)
    } catch (err) {
      setError(errMsg(err))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  // removeProject drops one row immediately (optimistic delete) so the
  // project disappears from home the moment the user confirms, instead of
  // waiting on the round-trip. The caller refetches after the server
  // answers, which restores the row if the delete actually failed.
  const removeProject = useCallback((id: string) => {
    setProjects((prev) => prev.filter((p) => p.id !== id))
  }, [])

  return { projects, loading, error, refresh, removeProject }
}
