// Shared API client: one fetch wrapper for JSON headers, !ok handling,
// and error extraction.

export function errMsg(err: unknown): string {
  return err instanceof Error ? err.message : String(err)
}

// projectPath builds an /api/projects/{id} URL with the id escaped —
// ids are owner/repo, and the slash must not split the route segment.
export function projectPath(id: string, suffix = ''): string {
  return `/api/projects/${encodeURIComponent(id)}${suffix}`
}

// PROBE_TIMEOUT_MS bounds provider probes client-side: it only fires when
// the answer is already lost.
const PROBE_TIMEOUT_MS = 90_000

export function probeSignal(): AbortSignal {
  return AbortSignal.timeout(PROBE_TIMEOUT_MS)
}

// probeErr maps a failed probe to a message with a timeout hint.
export function probeErr(err: unknown): string {
  if (err instanceof DOMException && err.name === 'TimeoutError') {
    return `Timed out after ${PROBE_TIMEOUT_MS / 1000}s. The provider may be slow or unreachable.`
  }
  return errMsg(err)
}
// ApiError carries the HTTP status and parsed body of a failed call, so
// callers can recover server-provided identity (e.g. a threadId on a
// failed turn).
export class ApiError extends Error {
  status: number
  body: Record<string, unknown> | null
  constructor(status: number, body: Record<string, unknown> | null, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.body = body
  }
}

// api calls the JSON API and returns the parsed body, or throws an
// ApiError carrying the server's message or the status.
export async function api<T = unknown>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    headers: {
      ...(init?.body ? { 'Content-Type': 'application/json' } : undefined),
      ...init?.headers,
    },
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    let body: Record<string, unknown> | null = null
    try {
      body = (await res.json()) as Record<string, unknown> | null
      if (body?.error) detail = String(body.error)
    } catch {
      // non-JSON error body — keep the status
    }
    throw new ApiError(res.status, body, detail)
  }
  if (res.status === 204) return undefined as T
  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}
