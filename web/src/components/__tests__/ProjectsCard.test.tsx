import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ProjectsCard } from '@/components/ProjectsCard'
import type { Project } from '@/lib/types'
import { mockFetch } from '@/test/mockFetch'

// Fast unit tests for the project list card. The backend is mocked at the
// fetch level; these edge cases (failed create keeps values, blank URL
// gating) are exactly what slow e2e tests are bad at pinning. One paste
// field, no method picker: the server normalizes every URL to SSH.

describe('ProjectsCard', () => {
  const PROJECTS: Project[] = [{ id: 'x/alpha' }]

  function renderCard(over: Partial<Parameters<typeof ProjectsCard>[0]> = {}) {
    const props = {
      projects: PROJECTS,
      loading: false,
      error: null,
      refresh: vi.fn(async () => {}),
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

  it('lists projects by owner/repo id', () => {
    renderCard()
    expect(screen.getByText('x/alpha')).toBeInTheDocument()
    expect(screen.getByTestId('project-card-x/alpha')).toBeInTheDocument()
  })

  it('surfaces a failed create and keeps the form values', async () => {
    vi.stubGlobal('fetch', mockFetch((url, init) =>
      url === '/api/projects' && init?.method === 'POST'
        ? { status: 409, body: { error: 'project already exists: "x/y"' } }
        : undefined))
    const { props } = renderCard()

    fireEvent.change(screen.getByPlaceholderText(/clone URL/i), { target: { value: 'https://github.com/x/y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'Clone project' }))
    expect(await screen.findByText(/project already exists/)).toHaveClass('text-destructive')
    expect(screen.getByPlaceholderText(/clone URL/i)).toHaveValue('https://github.com/x/y.git')
    expect(props.refresh).not.toHaveBeenCalled()
  })

  it('create success clears the form and refreshes', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/projects' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as { repoUrl: string }
        // the URL goes up as pasted — the server normalizes to SSH
        expect(body.repoUrl).toBe('https://github.com/x/y.git')
        return { status: 201, body: { id: 'x/y' } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    const { props } = renderCard()

    fireEvent.change(screen.getByPlaceholderText(/clone URL/i), { target: { value: 'https://github.com/x/y.git' } })
    fireEvent.click(screen.getByRole('button', { name: 'Clone project' }))
    await waitFor(() => expect(props.refresh).toHaveBeenCalled())
    expect(screen.getByPlaceholderText(/clone URL/i)).toHaveValue('')
    expect(screen.getByTestId('project-menu-x/alpha')).toBeInTheDocument()
  })

  it('disables clone while the repo URL is blank', () => {
    renderCard()
    expect(screen.getByRole('button', { name: 'Clone project' })).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText(/clone URL/i), { target: { value: 'git@github.com:x/y.git' } })
    expect(screen.getByRole('button', { name: 'Clone project' })).not.toBeDisabled()
  })
})
