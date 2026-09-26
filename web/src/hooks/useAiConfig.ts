import { useCallback, useState } from 'react'

import { api } from '@/lib/api'
import type { AIConfigStatus, AIModel } from '@/lib/types'

// useAiConfig reads the shared model list. Empty list disables codemap
// features everywhere; the head of the list is the active model.
export function useAiConfig() {
  const [status, setStatus] = useState<AIConfigStatus | null>(null)
  const [models, setModels] = useState<AIModel[]>([])
  const refresh = useCallback(async () => {
    try {
      const d = await api<{ models: AIModel[] }>('/api/ai/models')
      setModels(d.models ?? [])
      const first = (d.models ?? [])[0]
      const s = first
        ? { model: first.model, configured: true }
        : { model: '', configured: false }
      setStatus(s)
      return s
    } catch {
      return null
    }
  }, [])
  return { status, models, refresh }
}
