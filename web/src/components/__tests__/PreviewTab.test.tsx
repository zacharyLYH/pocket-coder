import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { act, render, screen, fireEvent, waitFor } from '@testing-library/react'
import { PreviewTab } from '@/components/terminal/PreviewTab'

// The ports list must survive a failed refresh: a transient probe error
// shouldn't blink live ports (and their start buttons) away mid-read.
describe('PreviewTab', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('keeps last-known ports when a refresh fails', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/preview/ports')) {
        calls += 1
        if (calls === 1) return new Response(JSON.stringify({ ports: [{ port: 3000, status: 'live' }] }), { status: 200 })
        throw new Error('probe hiccup')
      }
      if (u.endsWith('/preview/start')) return new Response(JSON.stringify({ ok: true }), { status: 200 })
      throw new Error(`unexpected fetch: ${u}`)
    }))

    render(<PreviewTab projectId="abc" onOpenPreview={vi.fn()} />)
    // First refresh populates (and auto-starts) :3000.
    await screen.findByTestId('preview-port-3000')

    // Manual refresh fails — the button must persist.
    fireEvent.click(screen.getByTestId('preview-refresh'))
    await waitFor(() => expect(calls).toBeGreaterThanOrEqual(2))
    expect(screen.getByTestId('preview-port-3000')).toBeInTheDocument()
  })

  // Every poll is a docker exec inside the project container, so a hidden
  // window must stop probing; returning to the tab refreshes right away.
  it('pauses polling while the window is hidden and refreshes on return', async () => {
    vi.useFakeTimers()
    let polls = 0
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/preview/ports')) {
        polls += 1
        return new Response(JSON.stringify({ ports: [] }), { status: 200 })
      }
      throw new Error(`unexpected fetch: ${u}`)
    }))
    let hidden = true
    vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => (hidden ? 'hidden' : 'visible'))

    render(<PreviewTab projectId="abc" onOpenPreview={vi.fn()} />)
    await act(async () => {})
    expect(polls).toBe(1)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(15_000)
    })
    expect(polls).toBe(1)

    hidden = false
    await act(async () => {
      document.dispatchEvent(new Event('visibilitychange'))
    })
    expect(polls).toBe(2)

    await act(async () => {
      await vi.advanceTimersByTimeAsync(5_000)
    })
    expect(polls).toBe(3)
  })

  // The sidecar's desktop is seeded by this request, so the viewport travels
  // with it. It renders at 1x (the desktop resizes live afterwards), so no
  // device pixel ratio belongs in the body.
  it('sends the client viewport with the start request', async () => {
    let startBody: Record<string, number> | null = null
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.endsWith('/preview/ports')) {
        return new Response(JSON.stringify({ ports: [{ port: 3000, status: 'live' }] }), { status: 200 })
      }
      if (u.endsWith('/preview/start')) {
        startBody = JSON.parse(init?.body as string) as Record<string, number>
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }
      throw new Error(`unexpected fetch: ${u}`)
    }))

    render(<PreviewTab projectId="abc" onOpenPreview={vi.fn()} />)
    await waitFor(() => expect(startBody).not.toBeNull())
    expect(startBody).toMatchObject({
      port: 3000,
      width: window.innerWidth,
      height: window.innerHeight,
    })
    expect(startBody).not.toHaveProperty('dpr')
    expect(startBody!.width).toBeGreaterThanOrEqual(100)
    expect(startBody!.height).toBeGreaterThanOrEqual(100)
  })

  it('auto-starts the lowest port, not ss-order first', async () => {
    const started: number[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const u = String(url)
      if (u.endsWith('/preview/ports')) {
        // ss order leads with ephemeral junk.
        return new Response(JSON.stringify({ ports: [{ port: 35827, status: 'live' }, { port: 3000, status: 'live' }] }), { status: 200 })
      }
      const m = u.match(/\/preview\/start$/)
      if (m && init?.method === 'POST') {
        started.push((JSON.parse(init.body as string) as { port: number }).port)
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }
      throw new Error(`unexpected fetch: ${u}`)
    }))

    render(<PreviewTab projectId="abc" onOpenPreview={vi.fn()} />)
    await waitFor(() => expect(started).toEqual([3000]))
  })
})
