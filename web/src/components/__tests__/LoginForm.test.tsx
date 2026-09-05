import { describe, expect, it, vi } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { LoginForm } from '@/components/LoginForm'
import { mockFetch } from '@/test/mockFetch'

// Unit tests for the login flow's request/verify steps and error surface.
describe('LoginForm', () => {
  it('moves to the PIN step after requesting a code, and shows request errors', async () => {
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/auth/request-pin' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        if (body.email === 'reject@example.com') return { status: 400, body: { error: 'unknown email' } }
        return { status: 200, body: { ok: true } }
      }
      if (url === '/api/auth/verify' && init?.method === 'POST') {
        return { status: 200, body: { email: JSON.parse(String(init.body)).email } }
      }
      return undefined
    }))
    const onLoggedIn = vi.fn()
    render(<LoginForm onLoggedIn={onLoggedIn} />)

    fireEvent.change(screen.getByPlaceholderText('you@example.com'), { target: { value: 'reject@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send code' }))
    // the request-pin step surfaces the HTTP status, not the body detail
    expect(await screen.findByText('HTTP 400')).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('6-digit PIN')).not.toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('you@example.com'), { target: { value: 'me@example.com' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send code' }))
    expect(await screen.findByPlaceholderText('6-digit PIN')).toBeInTheDocument()

    fireEvent.change(screen.getByPlaceholderText('6-digit PIN'), { target: { value: '123456' } })
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }))
    await waitFor(() => expect(onLoggedIn).toHaveBeenCalledWith('me@example.com'))
  })
})