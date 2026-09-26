import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { Home } from '@/components/Home'
import { mockFetch } from '@/test/mockFetch'

vi.mock('@/hooks/useProjects', () => ({
  useProjects: () => ({
    projects: [{ id: 'x/alpha' }, { id: 'x/beta' }],
    loading: false,
    error: null,
    refresh: vi.fn(async () => {}),
  }),
}))

vi.mock('@/hooks/useAiConfig', () => ({
  useAiConfig: () => ({ status: { configured: true }, refresh: vi.fn(async () => {}) }),
}))

// Unit tests for the home page's global Run card. The backend is mocked at
// the fetch level; the real fan-out path is covered by the Playwright suite.
describe('Home run card', () => {
  beforeEach(() => {
    Object.defineProperty(window, 'matchMedia', {
      writable: true,
      value: vi.fn().mockReturnValue({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }),
    })
  })

  function renderHome(execBody: unknown) {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/ssh-keys') return { status: 200, body: { keys: [] } }
      if (url === '/api/git/identities') return { status: 200, body: { identities: [{ id: 'g1' }] } }
      if (url === '/api/projects/exec') return { status: 200, body: execBody }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Home email="me@example.com" onLogout={vi.fn()} navigate={vi.fn()} />)
    return fetchMock
  }

  it('runs in every checked project and reports the outcome', async () => {
    const fetchMock = renderHome({ results: [{ project: 'x/alpha', status: 'ok' }, { project: 'x/beta', status: 'ok' }] })

    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'npm i -g x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run in checked projects' }))

    await screen.findByText('Ran in 2 projects.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/projects/exec'))
    expect(JSON.parse(String(call![1]?.body))).toEqual({ projectIds: ['x/alpha', 'x/beta'], command: 'npm i -g x' })
  })

  it('scopes the run to checked projects and surfaces per-project errors', async () => {
    const fetchMock = renderHome({ results: [{ project: 'x/beta', status: 'error', detail: 'boom' }] })

    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'echo hi' } })
    fireEvent.click(screen.getByLabelText('Run in x/alpha'))
    fireEvent.click(screen.getByRole('button', { name: 'Run in checked projects' }))

    await screen.findByText('x/beta: boom')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/projects/exec'))
    expect(JSON.parse(String(call![1]?.body))).toEqual({ projectIds: ['x/beta'], command: 'echo hi' })
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/api/projects/exec'))).toBe(true))
  })
})
