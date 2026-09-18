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

  it('round-trips special characters in project id and token', () => {
    const src = previewSurfacePath('owner/repo', 'a+b/c=d')
    const u = new URL(src, 'http://x')
    expect(u.searchParams.get('token')).toBe('a+b/c=d')
    expect(u.searchParams.get('path')).toContain('token=a%2Bb%2Fc%3Dd')
  })
})
