import { act, renderHook, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

import { useProjects } from '@/hooks/useProjects'
import { mockFetch } from '@/test/mockFetch'

// removeProject is the optimistic half of project deletion: it must drop the
// row locally with NO network call, so the row leaves home the instant the
// user confirms and only the reconciling refetch talks to the server.
describe('useProjects.removeProject', () => {
  it('drops the row locally without refetching', async () => {
    const fetchMock = mockFetch((url) =>
      url === '/api/projects'
        ? { status: 200, body: { projects: [{ id: 'x/alpha' }, { id: 'x/beta' }] } }
        : undefined,
    )
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useProjects())
    await waitFor(() => expect(result.current.projects).toHaveLength(2))
    const callsAfterLoad = fetchMock.mock.calls.length

    act(() => {
      result.current.removeProject('x/alpha')
    })

    expect(result.current.projects.map((p) => p.id)).toEqual(['x/beta'])
    expect(fetchMock.mock.calls.length).toBe(callsAfterLoad)
  })
})
