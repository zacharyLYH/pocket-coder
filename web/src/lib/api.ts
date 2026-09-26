// Shared API client: one fetch wrapper so components stop hand-rolling
// JSON headers, !ok handling, and error extraction. Shapes live in
// lib/types.ts.

export function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// projectPath builds an /api/projects/{id} URL with the id escaped —
// ids are owner/repo, and the slash must not split the route segment.
export function projectPath(id: string, suffix = ''): string {
  return `/api/projects/${encodeURIComponent(id)}${suffix}`
}

// PROBE_TIMEOUT_MS bounds provider probes client-side: the server allows
// 60s per live check, so this only fires when the answer is already lost
// and the button would otherwise sit on Testing... forever.
export const PROBE_TIMEOUT_MS = 90_000

// probeSignal bounds provider probes client-side.
export function probeSignal(): AbortSignal {
  return AbortSignal.timeout(PROBE_TIMEOUT_MS)
}

// probeErr maps a failed probe to a message: stalls surface as a timeout
// hint instead of a bare DOM error.
export function probeErr(err: unknown): string {
  if (err instanceof DOMException && err.name === 'TimeoutError') {
    return `Timed out after ${PROBE_TIMEOUT_MS / 1000}s. The provider may be slow or unreachable.`
  }
  return errMsg(err)
}
// ApiError carries the HTTP status and parsed body of a failed call, so
// callers can recover server-provided identity (e.g. a codemap threadId
// on a failed turn) instead of only seeing the message.
export class ApiError extends Error {
  status: number
  body: any
  constructor(status: number, body: any, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

// api calls the JSON API and returns the parsed body, or throws an
// ApiError carrying the server's message ("body.error") or the status.
export async function api<T = unknown>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
    ...init,
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    let body: any = null
    try {
      body = await res.json()
      if (body?.error) detail = body.error
    } catch {
      // non-JSON error body — keep the status
    }
    throw new ApiError(res.status, body, detail)
  }
  return res.json() as Promise<T>
}
