// postButlerTurn runs one butler turn over the shared SSE turn-stream:
// status lines via onStatus, then the final answer. Errors carry threadId
// when the server reserved a turn (same remount-into-run shape as codemap).
import { postTurnStream, isFinalShape, type StreamStatus } from '@/lib/turnStream'
import type { ButlerTurnResult } from '@/lib/types'

export type ButlerStatus = StreamStatus

export function postButlerTurn(
  body: { prompt: string; threadId?: string; projectHint?: string },
  onStatus?: (s: ButlerStatus) => void,
): Promise<ButlerTurnResult> {
  return postTurnStream<ButlerTurnResult>('/api/butler/turn', body, isFinalShape, onStatus)
}
