import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { PreviewTab } from '@/components/terminal/PreviewTab'

// The ports list must survive a failed refresh: a transient probe error
// shouldn't blink live ports (and their start buttons) away mid-read.
describe('PreviewTab', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
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
