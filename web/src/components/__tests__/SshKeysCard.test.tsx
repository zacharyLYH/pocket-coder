import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { SshKeysCard } from '@/components/SshKeysCard'
import type { SSHKey } from '@/lib/types'
import { mockFetch } from '@/test/mockFetch'

// Fast unit tests for the SSH key card. The backend is mocked at the fetch
// level; these edge cases (duplicate rejection, failed load) are exactly what
// slow e2e tests are bad at pinning.

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
