import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import App from '@/App'
import { mockFetch } from '@/test/mockFetch'

// Router pins: / is landing, /login is login, /app is the app. Logged-in
// users never sit on / or /login — the URL must say /app, not just render
// Home (back-button and refresh depend on the address bar being truthful).
describe('App routes', () => {
  beforeEach(() => {
    window.history.replaceState({}, '', '/')
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      if (url === '/api/auth/me') return { status: 200, body: { email: 'me@example.com' } }
      if (url === '/api/projects') return { status: 200, body: { projects: [] } }
      if (url === '/api/ssh/test' && init?.method === 'POST') return { status: 200, body: { ok: true, user: 'octocat' } }
      if (url === '/api/ai/models') return { status: 200, body: { models: [] } }
      return undefined
    }))
  })

  it('bounces a logged-in / to /app and shows home', async () => {
    window.history.replaceState({}, '', '/')
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/app'))
    expect(await screen.findByText('Run a command')).toBeInTheDocument()
  })

  it('bounces a logged-in /login to /app', async () => {
    window.history.replaceState({}, '', '/login')
    render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/app'))
    expect(await screen.findByText('Run a command')).toBeInTheDocument()
  })

  it('never touches history on re-render (redirects are effects, not render side effects)', async () => {
    window.history.replaceState({}, '', '/')
    const spy = vi.spyOn(window.history, 'replaceState')
    const { rerender } = render(<App />)
    await waitFor(() => expect(window.location.pathname).toBe('/app'))
    spy.mockClear()
    rerender(<App />)
    expect(spy).not.toHaveBeenCalled()
    spy.mockRestore()
  })
})
