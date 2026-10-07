import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { Home } from '@/components/Home'
import { mockFetch } from '@/test/mockFetch'
import { stubMatchMedia } from '@/test/stubs'

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
    stubMatchMedia()
  })

  function renderHome(execBody: unknown) {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/ssh/test' && init?.method === 'POST') return { status: 200, body: { ok: true, user: 'octocat' } }
      if (url === '/api/projects/exec') return { status: 200, body: execBody }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Home email="me@example.com" onLogout={vi.fn()} navigate={vi.fn()} />)
    return fetchMock
  }

  it('runs in every checked project and reports the outcome', async () => {
    const fetchMock = renderHome({ results: [{ project: 'x/alpha', status: 'ok' }, { project: 'x/beta', status: 'ok' }] })
    // content waits out the ssh probe gate before it renders
    await screen.findByText('Run a command')

    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'npm i -g x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run in checked projects' }))

    await screen.findByText('Ran in 2 projects.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/projects/exec'))
    expect(JSON.parse(String(call![1]?.body))).toEqual({ projectIds: ['x/alpha', 'x/beta'], command: 'npm i -g x' })
  })

  it('scopes the run to checked projects and surfaces per-project errors', async () => {
    const fetchMock = renderHome({ results: [{ project: 'x/beta', status: 'error', detail: 'boom' }] })
    await screen.findByText('Run a command')

    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'echo hi' } })
    fireEvent.click(screen.getByLabelText('Run in x/alpha'))
    fireEvent.click(screen.getByRole('button', { name: 'Run in checked projects' }))

    await screen.findByText('x/beta: boom')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/projects/exec'))
    expect(JSON.parse(String(call![1]?.body))).toEqual({ projectIds: ['x/beta'], command: 'echo hi' })
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/api/projects/exec'))).toBe(true))
  })

  it('gates the projects behind the connect card when the ssh probe fails', async () => {
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/ssh/test' && init?.method === 'POST') return { status: 502, body: { error: 'GitHub rejected the key' } }
      if (url === '/api/ssh') return { status: 200, body: { publicKey: 'ssh-ed25519 AAAAserver', fingerprint: 'SHA256:fp', createdAt: 't' } }
      return undefined
    }))
    render(<Home email="me@example.com" onLogout={vi.fn()} navigate={vi.fn()} />)

    expect(await screen.findByTestId('ssh-gate')).toBeInTheDocument()
    expect(screen.getByTestId('github-keys-link')).toHaveAttribute('href', 'https://github.com/settings/keys')
    expect(await screen.findByTestId('git-public-key')).toHaveTextContent('ssh-ed25519 AAAAserver')
    // no project surfaces while the key is unauthorized
    expect(screen.queryByText('Run a command')).not.toBeInTheDocument()
    expect(screen.queryByText('No projects yet.')).not.toBeInTheDocument()
  })

  it('unlocks on test success with no second probe', async () => {
    let probes = 0
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/ssh/test' && init?.method === 'POST') {
        probes += 1
        if (probes === 1) return { status: 502, body: { error: 'GitHub rejected the key' } }
        return { status: 200, body: { ok: true, user: 'octocat' } }
      }
      if (url === '/api/ssh') return { status: 200, body: { publicKey: 'ssh-ed25519 AAAAserver', fingerprint: 'SHA256:fp', createdAt: 't' } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<Home email="me@example.com" onLogout={vi.fn()} navigate={vi.fn()} />)

    expect(await screen.findByTestId('ssh-gate')).toBeInTheDocument()
    fireEvent.click(await screen.findByTestId('git-test'))
    await screen.findByText('Run a command')
    expect(probes).toBe(2)
  })
})
