// postButlerTurn runs one butler turn over the shared SSE turn-stream.
import { postTurnStream, isFinalShape, type StreamStatus } from '@/lib/turnStream'
import type { ButlerTurnResult } from '@/lib/types'

export type ButlerStatus = StreamStatus

export function postButlerTurn(
  body: { prompt: string; threadId?: string; projectHint?: string },
  onStatus?: (s: ButlerStatus) => void,
): Promise<ButlerTurnResult> {
  return postTurnStream<ButlerTurnResult>('/api/butler/turn', body, isFinalShape, onStatus)
}

// postButlerRetry reruns a thread's last failed turn over the same stream.
export function postButlerRetry(
  threadId: string,
  onStatus?: (s: ButlerStatus) => void,
): Promise<ButlerTurnResult> {
  return postTurnStream<ButlerTurnResult>(`/api/butler/threads/${encodeURIComponent(threadId)}/retry`, {}, isFinalShape, onStatus)
}
