import { describe, expect, it, vi, beforeEach } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { useQuickCommands } from '@/hooks/useQuickCommands'

describe('useQuickCommands', () => {
  beforeEach(() => {
    vi.unstubAllGlobals()
  })

  it('loads on mount and reloads on demand, keeping stale on failure', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(async () => {
      calls += 1
      if (calls === 1) return new Response(JSON.stringify({ quickCommands: { dev: 'npm run dev' } }), { status: 200 })
      throw new Error('down')
    }))

    const { result } = renderHook(() => useQuickCommands('abc'))
    await waitFor(() => expect(result.current.commands).toEqual({ dev: 'npm run dev' }))

    await act(async () => {
      await result.current.reload()
    })
    // Failed reload keeps the previous commands, and a null project fetches nothing.
    expect(result.current.commands).toEqual({ dev: 'npm run dev' })
    expect(calls).toBe(2)
  })

  it('fetches nothing for a null project', async () => {
    const fetch = vi.fn(async () => new Response('{}', { status: 200 }))
    vi.stubGlobal('fetch', fetch)
    renderHook(() => useQuickCommands(null))
    await new Promise((r) => setTimeout(r, 50))
    expect(fetch).not.toHaveBeenCalled()
  })
})
