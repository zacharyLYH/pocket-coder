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
