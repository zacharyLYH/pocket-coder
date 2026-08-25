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

  return { projects, loading, error, refresh }
}
