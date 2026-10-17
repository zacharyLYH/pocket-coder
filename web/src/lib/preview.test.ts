import { describe, expect, it } from 'vitest'

import { previewSurfacePath } from './preview'

// RFC §4: previewSurfacePath appends token= to the page URL AND embeds it
// in the websockify path value, so the noVNC websocket upgrade (which only
// sends the path value) carries the capability through the token gate.
describe('previewSurfacePath token embedding', () => {
  it('carries the token top-level for the page itself', () => {
    const src = previewSurfacePath('proj/1', 'tok123')
    const u = new URL(src, 'http://x')
    expect(u.searchParams.get('token')).toBe('tok123')
  })

  it('embeds the token inside the websockify path value', () => {
    const src = previewSurfacePath('proj/1', 'tok123')
    const u = new URL(src, 'http://x')
    const wsPath = u.searchParams.get('path') ?? ''
    // The WS upgrade replays this path as its request target, so the gate
    // (handlePreviewSurface, ?token=) must see the token there.
    expect(wsPath).toContain('websockify')
    expect(wsPath).toContain('token=tok123')
  })

  // The preview renders at 1x, so the desktop has to follow the iframe's CSS
  // box: the entry page turns `resize` into rfb.resizeSession. It has to be
  // our own page — vnc_lite.html only reads view_only and scale.
  it('points at the resize-aware entry page and asks for resize', () => {
    const u = new URL(previewSurfacePath('proj/1', 'tok123'), 'http://x')
    expect(u.pathname).toBe('/api/projects/proj%2F1/preview/pcoder.html')
    expect(u.searchParams.get('resize')).toBe('true')
    expect(u.searchParams.get('scale')).toBeNull()
  })

  it('round-trips special characters in project id and token', () => {
    const src = previewSurfacePath('owner/repo', 'a+b/c=d')
    const u = new URL(src, 'http://x')
    expect(u.searchParams.get('token')).toBe('a+b/c=d')
    expect(u.searchParams.get('path')).toContain('token=a%2Bb%2Fc%3Dd')
  })
})
