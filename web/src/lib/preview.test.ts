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

  // The framebuffer is the client viewport x DPR, so it can be bigger than
  // the iframe's CSS box. Without scaling — or with the param name that
  // vnc_lite.html ignores — the client shows only its top-left corner.
  it('asks the noVNC client to scale the framebuffer into the iframe', () => {
    const u = new URL(previewSurfacePath('proj/1', 'tok123'), 'http://x')
    expect(u.searchParams.get('scale')).toBe('true')
    expect(u.searchParams.get('resize')).toBeNull()
  })

  it('round-trips special characters in project id and token', () => {
    const src = previewSurfacePath('owner/repo', 'a+b/c=d')
    const u = new URL(src, 'http://x')
    expect(u.searchParams.get('token')).toBe('a+b/c=d')
    expect(u.searchParams.get('path')).toContain('token=a%2Bb%2Fc%3Dd')
  })
})
