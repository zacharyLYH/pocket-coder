import { useCallback, useState } from 'react'

import { api } from '@/lib/api'
import type { AIConfigStatus } from '@/lib/types'

// useAiConfig reads the single global model credential.
// configured=false disables codemap features everywhere.
export function useAiConfig() {
  const [status, setStatus] = useState<AIConfigStatus | null>(null)
  const refresh = useCallback(async () => {
    try {
      const d = await api<AIConfigStatus>('/api/ai/config')
      setStatus(d)
      return d
    } catch {
      return null
    }
  }, [])
  return { status, refresh }
}
