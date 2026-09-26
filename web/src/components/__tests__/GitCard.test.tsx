import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { GitCard } from '@/components/GitCard'
import { mockFetch } from '@/test/mockFetch'

// Mirrors the AICard contract: ungated save notifies the parent, deletes
// confirm with the blast radius.
describe('GitCard', () => {
  const ROW = [{ id: 'g1', label: 'work', name: 'N', email: 'n@e.com', hasToken: true }]

  function stub(initial: unknown[] = []) {
    let rows = initial
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/git/identities' && (init?.method ?? 'GET') === 'GET') {
        return { status: 200, body: { identities: rows } }
      }
      if (url === '/api/git/identities' && init?.method === 'POST') {
        rows = ROW
        return { status: 201, body: { id: 'g1' } }
      }
      if (url === '/api/git/identities/g1' && init?.method === 'DELETE') {
        rows = []
        return { status: 200, body: { ok: true } }
      }
      return undefined
    }))
  }

  it('saves without a prior test and lists the new row', async () => {
    stub()
    const onChanged = vi.fn()
    render(<GitCard onChanged={onChanged} />)
    await screen.findByTestId('git-card')

    fireEvent.change(screen.getByTestId('git-label'), { target: { value: 'work' } })
    fireEvent.change(screen.getByTestId('git-name'), { target: { value: 'N' } })
    fireEvent.change(screen.getByTestId('git-email'), { target: { value: 'n@e.com' } })
    fireEvent.change(screen.getByTestId('git-token'), { target: { value: 't' } })
    fireEvent.click(screen.getByTestId('git-save'))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('work')).toBeInTheDocument()
  })

  it('confirms deletes with the blast radius', async () => {
    stub(ROW)
    const onChanged = vi.fn()
    render(<GitCard onChanged={onChanged} />)
    await screen.findByDisplayValue('work')

    fireEvent.click(screen.getByRole('button', { name: 'Delete' }))
    expect(screen.getByDisplayValue('work')).toBeInTheDocument()
    expect(await screen.findByText(/push, and pull stop/)).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('git-confirm-delete'))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })
})
