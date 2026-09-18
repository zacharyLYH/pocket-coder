import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi, beforeEach } from 'vitest'

import { PreviewSurface } from '@/components/PreviewSurface'
import { PREVIEW_HEARTBEAT_MS } from '@/lib/preview'

describe('PreviewSurface', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  it('embeds the minted token and beats with it', async () => {
    const calls: { url: string; init?: RequestInit }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        calls.push({ url: String(url), init })
        if (String(url).endsWith('/preview')) {
          return new Response(JSON.stringify({ status: 'ready', token: 'tok123' }), { status: 200 })
        }
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }),
    )
    render(<PreviewSurface projectId="project/one" />)
    const frame = await screen.findByTitle('Remote project preview')
    const src = frame.getAttribute('src') ?? ''
    expect(src).toContain('/api/projects/project%2Fone/preview/vnc_lite.html?')
    expect(src).toContain('token=tok123')
    await waitFor(() => {
      expect(calls.some((c) => String(c.url).endsWith('/preview/heartbeat'))).toBe(true)
    })
    const beat = calls.find((c) => String(c.url).endsWith('/preview/heartbeat'))
    expect((beat?.init?.headers as Record<string, string>)['X-Preview-Token']).toBe('tok123')
    expect(PREVIEW_HEARTBEAT_MS).toBeGreaterThan(0)
  })

  it('shows expired UI and resumes with a fresh token', async () => {
    // Faithful server model: one live token at a time. The first beat
    // arrives after a server-side rotation, so it 404s; the post-Resume
    // beat carries the fresh token and must 200, or Resume would race
    // straight back into the expired card.
    let live = 'tok1'
    let rotated = false
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (String(url).endsWith('/preview/heartbeat')) {
          const sent = (init?.headers as Record<string, string> | undefined)?.['X-Preview-Token']
          if (sent === live && !rotated) {
            rotated = true
            live = 'tok2'
            return new Response(JSON.stringify({ error: 'not found' }), { status: 404 })
          }
          if (sent === live) return new Response(JSON.stringify({ ok: true }), { status: 200 })
          return new Response(JSON.stringify({ error: 'not found' }), { status: 404 })
        }
        if (String(url).endsWith('/preview')) {
          return new Response(JSON.stringify({ status: 'ready', token: live }), {
            status: 200,
          })
        }
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }),
    )
    render(<PreviewSurface projectId="abc" />)
    await screen.findByText('Preview expired — the sidecar rotated its token.')
    fireEvent.click(screen.getByText('Resume'))
    await screen.findByTitle('Remote project preview')
    expect(screen.getByTitle('Remote project preview').getAttribute('src')).toContain('token=tok2')
  })

  it('syncs the fit only after the surface loads, with the token header', async () => {
    const posts: { url: string; init?: RequestInit }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string, init?: RequestInit) => {
        if (String(url).endsWith('/preview')) {
          return new Response(JSON.stringify({ status: 'ready', token: 'tok9' }), { status: 200 })
        }
        posts.push({ url: String(url), init })
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }),
    )
    vi.stubGlobal('ResizeObserver', class {
      observe() {}
      disconnect() {}
    })

    render(<PreviewSurface projectId="abc" />)
    const frame = await screen.findByTitle('Remote project preview')
    // clientWidth is 0 in jsdom; define a plausible box.
    Object.defineProperty(frame, 'clientWidth', { value: 800 })
    Object.defineProperty(frame, 'clientHeight', { value: 600 })
    await new Promise((r) => setTimeout(r, 10))
    expect(posts.filter((p) => p.url.endsWith('/preview/tools/viewport'))).toEqual([])

    fireEvent.load(frame)
    await new Promise((r) => setTimeout(r, 10))
    const fits = posts.filter((p) => p.url.endsWith('/preview/tools/viewport'))
    expect(fits).toHaveLength(1)
    expect((fits[0].init?.headers as Record<string, string>)['X-Preview-Token']).toBe('tok9')
  })

  it('shows a not-running card instead of a blank page when stopped', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(JSON.stringify({ status: 'stopped' }), { status: 200 })),
    )
    render(<PreviewSurface projectId="abc" />)
    await screen.findByText(/not running/i)
    expect(screen.queryByTitle('Remote project preview')).toBeNull()
  })

  it('stops beating once the token expires', async () => {
    const clearSpy = vi.spyOn(globalThis, 'clearInterval')
    vi.stubGlobal(
      'fetch',
      vi.fn(async (url: string) => {
        if (String(url).endsWith('/preview')) {
          return new Response(JSON.stringify({ status: 'ready', token: 'tokX' }), { status: 200 })
        }
        return new Response(JSON.stringify({ error: 'not found' }), { status: 404 })
      }),
    )
    render(<PreviewSurface projectId="abc" />)
    await screen.findByText('Preview expired — the sidecar rotated its token.')
    // Expiry must tear down the heartbeat interval; otherwise the dead
    // token keeps hammering the server with 404s until Resume.
    await waitFor(() => {
      expect(clearSpy).toHaveBeenCalled()
    })
    clearSpy.mockRestore()
  })
})
