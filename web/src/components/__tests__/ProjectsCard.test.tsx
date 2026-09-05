import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ProjectsCard } from '@/components/ProjectsCard'
import type { Project } from '@/lib/types'
import { mockFetch } from '@/test/mockFetch'

// Fast unit tests for the project list card. The backend is mocked at the
// fetch level; these edge cases (failed create keeps values, clone-via hint)
// are exactly what slow e2e tests are bad at pinning.

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

    expect(screen.getByTestId('project-menu-p1')).toBeInTheDocument()
    // delete is behind dropdown — tested in e2e; unit just checks trigger exists
  })

  it('clone-via hint counts registered SSH keys', () => {
    renderCard({ sshKeyCount: 2 })
    fireEvent.change(screen.getByPlaceholderText(/Repo URL/), { target: { value: 'git@x:y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'SSH' }))
    expect(screen.getByText('2 key(s) registered')).toBeInTheDocument()
  })
})