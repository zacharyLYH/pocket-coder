// Shared SSE turn-stream client: parses status lines (data: {...}) via
// onStatus, resolves with the single bare-JSON final. Any chatbot reuses
// this; callers only supply path + body + final-shape check.
import { ApiError } from '@/lib/api'

export type StreamStatus = { tool: string; status: string }

// isFinalShape is the shared final/error discriminator: a final answer
// carries threadId+answer, an error body carries an error string.
export function isFinalShape(v: Record<string, unknown>): boolean {
  return typeof v.threadId === 'string' && typeof v.answer === 'string'
}

export async function postTurnStream<TFinal>(
  path: string,
  body: unknown,
  isFinal: (v: Record<string, unknown>) => boolean,
  onStatus?: (s: StreamStatus) => void,
): Promise<TFinal> {
  const res = await fetch(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  const text = await res.text()
  let final: TFinal | null = null
  let sawError: { error: string } | null = null
  for (const line of text.split('\n')) {
    const t = line.trim()
    if (!t) continue
    if (t.startsWith('data:')) {
      // The server only ever writes statuses behind data:; the final is
      // a bare JSON line. Unparseable-after-prefix falls through to the
      // whole-line parse below as a last resort.
      try {
        const parsed = JSON.parse(t.slice(5)) as Record<string, unknown>
        if (isFinal(parsed)) final = parsed as unknown as TFinal
        else if (typeof parsed.error === 'string') sawError = { error: parsed.error }
        else onStatus?.(parsed as StreamStatus)
        continue
      } catch {
        // fall through: try the whole line as final
      }
    }
    try {
      const parsed = JSON.parse(t) as Record<string, unknown>
      if (isFinal(parsed)) final = parsed as unknown as TFinal
      else if (typeof parsed.error === 'string') sawError = { error: parsed.error }
    } catch {
      // non-JSON line — ignore (whitespace/heartbeat)
    }
  }
  if (final && res.ok) return final
  if (sawError) throw new ApiError(res.status, sawError, sawError.error)
  try {
    const parsed = JSON.parse(text) as { error?: string }
    if (parsed?.error) throw new ApiError(res.status, parsed, parsed.error)
  } catch (e) {
    if (e instanceof ApiError) throw e
  }
  throw new ApiError(res.status, null, `HTTP ${res.status}`)
}
