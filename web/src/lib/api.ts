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

// api calls the JSON API and returns the parsed body, or throws an Error
// carrying the server's message ("body.error") or the HTTP status.
export async function api<T = unknown>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: init?.body ? { 'Content-Type': 'application/json' } : undefined,
    ...init,
  })
  if (!res.ok) {
    let detail = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (body?.error) detail = body.error
    } catch {
      // non-JSON error body — keep the status
    }
    throw new Error(detail)
  }
  return res.json() as Promise<T>
}
