import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { SshKeysCard } from '@/components/SshKeysCard'
import type { SSHKey } from '@/lib/types'
import { ProjectsCard } from '@/components/ProjectsCard'
import type { Project } from '@/lib/types'

// Fast unit tests for the two list-management cards. The backend is mocked
// at the fetch level; the edge cases here (duplicate rejection, failed
// create, load errors) are exactly what slow e2e tests are bad at pinning.

function mockFetch(handler: (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const out = handler(url, init)
    if (!out) throw new Error(`unexpected fetch: ${url}`)
    return new Response(JSON.stringify(out.body), { status: out.status })
  })
}

describe('SshKeysCard', () => {
  const KEYS: SSHKey[] = [{ fingerprint: 'sha256-abc', publicKey: 'ssh-ed25519 AAAA', label: 'laptop' }]

  it('shows the empty state and then registered keys', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/ssh-keys' ? { status: 200, body: { keys: [] } } : undefined))
    const { rerender } = render(<SshKeysCard keys={[]} onChanged={() => {}} />)
    expect(screen.getByText('No keys registered.')).toBeInTheDocument()

    rerender(<SshKeysCard keys={KEYS} onChanged={() => {}} />)
    expect(screen.getByText('laptop')).toBeInTheDocument()
    expect(screen.queryByText('No keys registered.')).not.toBeInTheDocument()
  })

  it('clears the form on success and surfaces duplicate rejection', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/ssh-keys' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        if (body.publicKey === 'dup') return { status: 400, body: { error: 'key already registered' } }
        return { status: 201, body: { fingerprint: 'sha256-new' } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const onChanged = vi.fn()
    render(<SshKeysCard keys={[]} onChanged={onChanged} />)

    const input = screen.getByPlaceholderText(/ssh-ed25519/)
    fireEvent.change(input, { target: { value: 'ssh-ed25519 AAAA-new' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add key' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(input).toHaveValue('')

    // duplicate: error shown, no refresh
    fireEvent.change(input, { target: { value: 'dup' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add key' }))
    expect(await screen.findByText('key already registered')).toHaveClass('text-destructive')
    expect(onChanged).toHaveBeenCalledTimes(1)
  })

  it('delete calls the API and refreshes', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/ssh-keys/sha256-abc' && init?.method === 'DELETE') return { status: 200, body: { ok: true } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const onChanged = vi.fn()
    render(<SshKeysCard keys={KEYS} onChanged={onChanged} />)
    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })
})

describe('ProjectsCard', () => {
  const PROJECTS: Project[] = [{ id: 'p1', name: 'alpha' }]

  function renderCard(over: Partial<Parameters<typeof ProjectsCard>[0]> = {}) {
    const props = {
      projects: PROJECTS,
      loading: false,
      error: null,
      refresh: vi.fn(async () => {}),
      sshKeyCount: 0,
      navigate: vi.fn(),
      ...over,
    }
    const utils = render(<ProjectsCard {...props} />)
    return { props, ...utils }
  }

  it('shows loading, error, and empty states', () => {
    const { unmount } = renderCard({ loading: true })
    expect(screen.getByText('Loading projects…')).toBeInTheDocument()
    unmount()

    renderCard({ loading: false, error: 'HTTP 500' })
    expect(screen.getByText(/Failed to load projects: HTTP 500/)).toBeInTheDocument()
  })

  it('surfaces a failed create and keeps the form values', async () => {
    vi.stubGlobal('fetch', mockFetch((url, init) =>
      url === '/api/projects' && init?.method === 'POST'
        ? { status: 409, body: { error: 'clone failed: repo not found' } }
        : undefined))
    const { props } = renderCard()

    fireEvent.change(screen.getByPlaceholderText(/Repo URL/), { target: { value: 'https://x/y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))
    expect(await screen.findByText(/clone failed/)).toHaveClass('text-destructive')
    expect(screen.getByPlaceholderText(/Repo URL/)).toHaveValue('https://x/y.git')
    expect(props.refresh).not.toHaveBeenCalled()
  })

  it('create success clears the form and refreshes; delete confirms first', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/projects' && init?.method === 'POST') return { status: 201, body: { id: 'p2' } }
      if (url === '/api/projects/p1' && init?.method === 'DELETE') return { status: 200, body: { ok: true } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const { props } = renderCard({ sshKeyCount: 2 })

    fireEvent.change(screen.getByPlaceholderText(/Repo URL/), { target: { value: 'https://x/y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'HTTPS' }).nextSibling as Element) // SSH toggle
    fireEvent.click(screen.getByRole('button', { name: 'Create project' }))
    await waitFor(() => expect(props.refresh).toHaveBeenCalled())
    expect(screen.getByPlaceholderText(/Repo URL/)).toHaveValue('')

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(fetchMock.mock.calls.some(([u, i]) => String(u).includes('p1') && i?.method === 'DELETE')).toBe(false)
  })

  it('clone-via hint counts registered SSH keys', () => {
    renderCard({ sshKeyCount: 2 })
    fireEvent.change(screen.getByPlaceholderText(/Repo URL/), { target: { value: 'git@x:y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'SSH' }))
    expect(screen.getByText('2 key(s) registered')).toBeInTheDocument()
  })
})
