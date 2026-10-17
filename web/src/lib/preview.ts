export function previewSurfacePath(projectId: string, token: string): string {
  // The noVNC client opens its websocket from the `path` value alone — the
  // page's top-level query is NOT forwarded — so the capability must ride
  // inside the path value or the websockify upgrade hits the token gate
  // without a token (404) and the surface never connects.
  const websocketPath = `api/projects/${encodeURIComponent(projectId)}/preview/websockify?token=${encodeURIComponent(token)}`
  // pcoder.html is our trimmed noVNC entry page (served from the sidecar
  // image). It reads `resize` and asks the VNC server to resize the desktop
  // to the iframe's box on every sync.
  const page = `/api/projects/${encodeURIComponent(projectId)}/preview/pcoder.html`
  const q = new URLSearchParams({
    resize: 'true',
    reconnect: '1',
    reconnect_delay: '2000',
    path: websocketPath,
    token,
  })
  return `${page}?${q.toString()}`
}

export const PREVIEW_HEARTBEAT_MS = Number(import.meta.env.VITE_PREVIEW_HEARTBEAT_MS ?? 30_000)
