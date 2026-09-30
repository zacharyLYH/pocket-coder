import { afterEach, describe, expect, it, vi } from 'vitest'

import { api, errMsg, probeErr, probeSignal } from '@/lib/api'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('probe helpers', () => {
  it('maps aborts to a timeout hint and passes other errors through', () => {
    const timeout = new DOMException('The operation timed out.', 'TimeoutError')
    expect(probeErr(timeout)).toContain('Timed out after 90s')
    expect(probeErr(new Error('boom'))).toBe('boom')
    expect(errMsg(timeout)).not.toContain('Timed out')
  })

  it('returns a live signal', () => {
    const s = probeSignal()
    expect(s).toBeInstanceOf(AbortSignal)
    expect(s.aborted).toBe(false)
  })
})

describe('api headers and empty bodies', () => {
  it('keeps Content-Type when callers pass custom headers', async () => {
    let seen: Record<string, string> = {}
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init?: RequestInit) => {
        seen = Object.fromEntries(new Headers(init?.headers).entries())
        return new Response(JSON.stringify({ ok: true }), { status: 200 })
      }),
    )
    await api('/x', { method: 'POST', body: JSON.stringify({}), headers: { 'X-Preview-Token': 't' } })
    expect(seen['content-type']).toContain('application/json')
    expect(seen['x-preview-token']).toBe('t')
  })

  it('resolves undefined on 204 and empty 200 instead of throwing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
    await expect(api('/empty')).resolves.toBeUndefined()
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 200 })))
    await expect(api('/void')).resolves.toBeUndefined()
  })
})
