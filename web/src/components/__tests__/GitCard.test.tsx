import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { GitCard } from '@/components/GitCard'
import { mockFetch } from '@/test/mockFetch'

// The card is the server deploy key: display, probe, rotate. The private
// half never renders; a passed probe notifies the parent, because the
// home gate unlocks on it.
describe('GitCard', () => {
  const KEY = { publicKey: 'ssh-ed25519 AAAAserverkey', fingerprint: 'SHA256:oldfp', createdAt: '2026-01-01T00:00:00Z' }

  function stub(opts: { testFails?: boolean } = {}) {
    let key = KEY
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/ssh' && (init?.method ?? 'GET') === 'GET') return { status: 200, body: key }
      if (url === '/api/ssh/test' && init?.method === 'POST') {
        if (opts.testFails) return { status: 502, body: { error: 'GitHub rejected the key — add the public half to GitHub first' } }
        return { status: 200, body: { ok: true, user: 'octocat' } }
      }
      if (url === '/api/ssh/regenerate' && init?.method === 'POST') {
        key = { publicKey: 'ssh-ed25519 AAAAnewkey', fingerprint: 'SHA256:newfp', createdAt: '2026-01-02T00:00:00Z' }
        return { status: 200, body: key }
      }
      return undefined
    }))
  }

  it('displays the public key and fingerprint', async () => {
    stub()
    render(<GitCard />)
    expect(await screen.findByTestId('git-public-key')).toHaveTextContent('ssh-ed25519 AAAAserverkey')
    expect(screen.getByTestId('git-fingerprint')).toHaveTextContent('SHA256:oldfp')
  })

  it('test unlocks the parent with no re-probe', async () => {
    stub()
    const onTestSuccess = vi.fn()
    const onChanged = vi.fn()
    render(<GitCard onChanged={onChanged} onTestSuccess={onTestSuccess} />)
    await screen.findByTestId('git-public-key')

    fireEvent.click(screen.getByTestId('git-test'))
    expect(await screen.findByTestId('git-tested')).toHaveTextContent('Authenticated as octocat')
    await waitFor(() => expect(onTestSuccess).toHaveBeenCalled())
    expect(onChanged).not.toHaveBeenCalled()
  })

  it('test failure surfaces the probe error without notifying', async () => {
    stub({ testFails: true })
    const onTestSuccess = vi.fn()
    render(<GitCard onTestSuccess={onTestSuccess} />)
    await screen.findByTestId('git-public-key')

    fireEvent.click(screen.getByTestId('git-test'))
    expect(await screen.findByTestId('git-error')).toHaveTextContent('add the public half to GitHub first')
    expect(onTestSuccess).not.toHaveBeenCalled()
  })

  it('hides regenerate when showRegenerate is false', () => {
    stub()
    render(<GitCard showRegenerate={false} />)
    expect(screen.queryByTestId('git-regen')).not.toBeInTheDocument()
  })

  it('regenerate asks for confirmation, then swaps the key and notifies', async () => {
    stub()
    const onChanged = vi.fn()
    render(<GitCard onChanged={onChanged} />)
    await screen.findByTestId('git-public-key')

    fireEvent.click(screen.getByTestId('git-regen'))
    expect(await screen.findByText('Regenerate server key?')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('git-confirm-regen'))

    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    expect(await screen.findByTestId('git-fingerprint')).toHaveTextContent('SHA256:newfp')
  })
})
