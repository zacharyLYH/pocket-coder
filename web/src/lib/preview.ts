export function previewSurfacePath(projectId: string, token: string): string {
  // The noVNC client opens its websocket from the `path` value alone — the
  // page's top-level query is NOT forwarded — so the capability must ride
  // inside the path value or the websockify upgrade hits the token gate
  // without a token (404) and the surface never connects.
  const websocketPath = `api/projects/${encodeURIComponent(projectId)}/preview/websockify?token=${encodeURIComponent(token)}`
  const page = `/api/projects/${encodeURIComponent(projectId)}/preview/vnc_lite.html`
  const q = new URLSearchParams({
    autoconnect: 'true',
    // `scale`, NOT `resize`: vnc_lite.html reads `scale` into
    // rfb.scaleViewport and ignores anything else (verified against the
    // sidecar image: `resize=scale` left the canvas at framebuffer size and
    // silently cropped it to the iframe). Scaling is what lets the framebuffer
    // be the viewport x DPR — a 2x phone gets a 2x render fitted to its box
    // instead of the top-left quarter of a bigger screen.
    scale: 'true',
    reconnect: '1',
    reconnect_delay: '2000',
    path: websocketPath,
    token,
  })
  // vnc_lite.html is minimal without sidebar; fallback to vnc.html with hidden bar if lite not available
  return `${page}?${q.toString()}`
}

export const PREVIEW_HEARTBEAT_MS = Number(import.meta.env.VITE_PREVIEW_HEARTBEAT_MS ?? 30_000)
