import { api } from '@/lib/api'
import type { ButlerTurnResult } from '@/lib/types'

export function postButlerTurn(body: { prompt: string; threadId?: string; projectHint?: string }): Promise<ButlerTurnResult> {
  return api<ButlerTurnResult>('/api/butler/turn', { method: 'POST', body: JSON.stringify(body) })
}

export function postButlerRetry(threadId: string): Promise<ButlerTurnResult> {
  return api<ButlerTurnResult>(`/api/butler/threads/${encodeURIComponent(threadId)}/retry`, { method: 'POST', body: '{}' })
}
