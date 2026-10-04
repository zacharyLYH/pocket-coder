import { render } from '@testing-library/react'
import { act } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { ShortcutsModal } from '@/components/shortcuts/ShortcutsModal'
import { mockFetch } from '@/test/mockFetch'

// Regression test for the duplicate GET /api/projects/{id} fetch that
// happened when ShortcutsModal both called reload() AND useShortcuts'
// internal effect fired on the same projectId change.
// The modal should trigger exactly one fetch per open transition.
describe('ShortcutsModal fetch behavior', () => {
  it('fires exactly one GET when the modal opens', async () => {
    const calls: { url: string; method: string }[] = []
    const fetchMock = mockFetch((url, init) => {
      const method = init?.method ?? 'GET'
      calls.push({ url, method })
      if (url === '/api/projects/my%2Fproject' && method === 'GET') {
        return { status: 200, body: { shortcuts: [{ id: 's-dev', alias: 'dev', kind: 'cmd', command: 'npm run dev' }] } }
      }
      throw new Error(`unexpected: ${method} ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    // Start with modal closed, then open it
    const onOpenChange = vi.fn()
    const { rerender } = render(
      <ShortcutsModal projectId="my/project" open={false} onOpenChange={onOpenChange} />
    )

    // Let initial mount settle (should be no fetch since open=false)
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50))
    })

    // Now open the modal
    rerender(<ShortcutsModal projectId="my/project" open={true} onOpenChange={onOpenChange} />)

    // Wait for fetch to complete
    await act(async () => {
      await new Promise((r) => setTimeout(r, 200))
    })

    const gets = calls.filter((c) => c.url === '/api/projects/my%2Fproject' && c.method === 'GET')
    expect(gets).toHaveLength(1)

    vi.restoreAllMocks()
  })

  it('fires no GET when closed', async () => {
    const calls: { url: string; method: string }[] = []
    const fetchMock = mockFetch((url, init) => {
      const method = init?.method ?? 'GET'
      calls.push({ url, method })
      if (url === '/api/projects/my%2Fproject' && method === 'GET') {
        return { status: 200, body: { shortcuts: [] } }
      }
      throw new Error(`unexpected: ${method} ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ShortcutsModal projectId="my/project" open={false} onOpenChange={vi.fn()} />)

    await act(async () => {
      await new Promise((r) => setTimeout(r, 100))
    })

    const gets = calls.filter((c) => c.url === '/api/projects/my%2Fproject' && c.method === 'GET')
    expect(gets).toHaveLength(0)

    vi.restoreAllMocks()
  })
})
