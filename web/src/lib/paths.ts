// Terminal URLs look like /projects/{id}/terminal/{session}; anything else
// is the home page.
export function terminalPath(projectId: string, session: string): string {
  return `/projects/${encodeURIComponent(projectId)}/terminal/${encodeURIComponent(session)}`
}

export function parseTerminalPath(path: string): { projectId: string; session: string } | null {
  const m = path.match(/^\/projects\/([^/]+)\/terminal\/([^/]+)$/)
  return m ? { projectId: decodeURIComponent(m[1]), session: decodeURIComponent(m[2]) } : null
}
