export function previewPath(projectId: string): string {
  return `/preview/${encodeURIComponent(projectId)}`
}

export function previewSurfacePath(projectId: string): string {
  const websocketPath = `api/projects/${encodeURIComponent(projectId)}/preview/websockify`
  // vnc_lite.html is minimal without sidebar; fallback to vnc.html with hidden bar if lite not available
  return `/api/projects/${encodeURIComponent(projectId)}/preview/vnc_lite.html?autoconnect=true&resize=scale&reconnect=1&reconnect_delay=2000&path=${encodeURIComponent(websocketPath)}`
}
