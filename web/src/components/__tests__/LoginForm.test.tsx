import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { LoginForm } from '@/components/LoginForm'
import { mockFetch } from '@/test/mockFetch'
import { stubMatchMedia } from '@/test/stubs'

// Unit tests for the email + password login: one generic error for every
// failure, success hands the email up to the app session flow.
describe('LoginForm', () => {
  it('logs in with email and password', async () => {
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/auth/login' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        if (body.password === 'wrong') return { status: 401, body: { error: 'invalid email or password' } }
        return { status: 200, body: { email: body.email } }
      }
      return undefined
    }))
    const onLoggedIn = vi.fn()
    render(<LoginForm onLoggedIn={onLoggedIn} />)

    fireEvent.change(screen.getByPlaceholderText('you@example.com'), { target: { value: 'me@example.com' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'wrong' } })
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    // Invalid credentials surface ONE generic message — never which half failed.
    expect(await screen.findByText('Invalid email or password.')).toBeInTheDocument()
    expect(onLoggedIn).not.toHaveBeenCalled()

    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'correct-password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(onLoggedIn).toHaveBeenCalledWith('me@example.com'))
  })

  it('shows the same generic error when the server is unreachable', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => { throw new Error('network down') }))
    render(<LoginForm onLoggedIn={vi.fn()} />)
    fireEvent.change(screen.getByPlaceholderText('you@example.com'), { target: { value: 'me@example.com' } })
    fireEvent.change(screen.getByPlaceholderText('Password'), { target: { value: 'whatever' } })
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    expect(await screen.findByText('Invalid email or password.')).toBeInTheDocument()
  })
})

// PWA card pins: an install prompt that fired before React mounted (the
// inline index.html stash) must still offer Install, and inside the
// installed app the card must not dead-end on "no prompt".
describe('LoginForm PWA card', () => {
  beforeEach(() => {
    delete (window as unknown as Record<string, unknown>).__pwaInstallEvent
  })

  it('offers install from a pre-mount stashed prompt', async () => {
    ;(window as unknown as Record<string, unknown>).__pwaInstallEvent = {
      prompt: vi.fn(async () => {}),
      userChoice: Promise.resolve({ outcome: 'accepted' }),
    }
    render(<LoginForm onLoggedIn={vi.fn()} />)
    expect(await screen.findByRole('button', { name: 'Install Pocket Coder' })).toBeInTheDocument()
  })

  it('reports installed when running standalone', async () => {
    stubMatchMedia(true)
    render(<LoginForm onLoggedIn={vi.fn()} />)
    expect(await screen.findByText('Installed. Open it from your home screen.')).toBeInTheDocument()
    Object.defineProperty(window, 'matchMedia', { writable: true, value: undefined })
  })
})
