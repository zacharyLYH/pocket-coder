// Terminal URLs look like /projects/{id}/terminal/{session}; anything else
// is the home page.
export function terminalPath(projectId: string, session: string): string {
  return `/projects/${encodeURIComponent(projectId)}/terminal/${encodeURIComponent(session)}`
}

// safeDecode never throws: malformed escapes (e.g. /preview/%zz) yield
// null instead of crashing the router.
export function safeDecode(segment: string): string | null {
  try {
    return decodeURIComponent(segment)
  } catch {
    return null
  }
}

export function parseTerminalPath(path: string): { projectId: string; session: string } | null {
  const m = path.match(/^\/projects\/([^/]+)\/terminal\/([^/]+)$/)
  if (!m) return null
  const projectId = safeDecode(m[1])
  const session = safeDecode(m[2])
  return projectId === null || session === null ? null : { projectId, session }
}

export function parsePreviewPath(path: string): string | null {
  const m = path.match(/^\/preview\/([^/]+)$/)
  if (!m) return null
  return safeDecode(m[1])
}
