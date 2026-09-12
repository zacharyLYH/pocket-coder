import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'

import { PreviewSurface } from '@/components/PreviewSurface'

describe('PreviewSurface', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('loads the authenticated PCODER surface, not the project URL', () => {
    render(<PreviewSurface projectId="project/one" />)
    const frame = screen.getByTitle('Remote project preview')
    expect(frame).toHaveAttribute('src', '/api/projects/project%2Fone/preview/vnc_lite.html?autoconnect=true&resize=scale&reconnect=1&reconnect_delay=2000&path=api%2Fprojects%2Fproject%252Fone%2Fpreview%2Fwebsockify')
  })

  it('syncs the fit only after the surface loads', async () => {
    const posts: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      posts.push(String(url))
      return new Response(JSON.stringify({ ok: true }), { status: 200 })
    }))
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      disconnect() {}
    })

    render(<PreviewSurface projectId="abc" />)
    const frame = screen.getByTitle('Remote project preview')
    // clientWidth is 0 in jsdom; define a plausible box.
    Object.defineProperty(frame, 'clientWidth', { value: 800 })
    Object.defineProperty(frame, 'clientHeight', { value: 600 })
    await new Promise((r) => setTimeout(r, 10))
    expect(posts).toEqual([])

    fireEvent.load(frame)
    await new Promise((r) => setTimeout(r, 10))
    expect(posts).toEqual(['/api/projects/abc/preview/tools/viewport'])
  })
})
