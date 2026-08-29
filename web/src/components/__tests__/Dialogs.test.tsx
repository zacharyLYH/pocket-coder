import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { NewSessionDialog } from '@/components/terminal/NewSessionDialog'
import { ProjectPicker } from '@/components/HarnessesCard'
import { LoginForm } from '@/components/LoginForm'

// Unit tests for the terminal dialog and the shared picker — the pieces
// with real logic (name-required, installed-only filter, launch timeout, selection scoping)
// and for the login form's error paths. Fetch is mocked; the real terminal
// bridge is covered by the Playwright stack tests.

function mockFetch(handler: (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const out = handler(url, init)
    if (!out) throw new Error(`unexpected fetch: ${url}`)
    return new Response(JSON.stringify(out.body), { status: out.status })
  })
}

beforeEach(() => {
  window.confirm = vi.fn(() => true)
})

const HARNESS_INSTALLED = [{ id: 'opencode', name: 'OpenCode', command: 'opencode', install: 'npm i -g opencode-ai', installed: true }]
const HARNESS_NOT_INSTALLED = [{ id: 'opencode', name: 'OpenCode', command: 'opencode', install: 'npm i -g opencode-ai', installed: false }]

describe('NewSessionDialog', () => {
  function renderDialog(over: Partial<Parameters<typeof NewSessionDialog>[0]> = {}) {
    const props = {
      open: true,
      onOpenChange: vi.fn(),
      projectId: 'p1',
      harnesses: HARNESS_INSTALLED,
      onLaunched: vi.fn(),
      ...over,
    }
    render(<NewSessionDialog {...props} />)
    return props
  }

  it('shell sessions post the typed name', async () => {
    const fetchMock = mockFetch((url, init) =>
      url === '/api/projects/p1/sessions' && init?.method === 'POST'
        ? { status: 201, body: { name: 'dev' } }
        : undefined)
    vi.stubGlobal('fetch', fetchMock)
    const props = renderDialog()

    fireEvent.change(screen.getByPlaceholderText(/Session name/), { target: { value: 'dev' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create & Attach' }))
    await waitFor(() => expect(props.onLaunched).toHaveBeenCalledWith('dev'))
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body)).name).toBe('dev')
  })

  it('harness sessions post the typed name and harness id', async () => {
    const fetchMock = mockFetch((url, init) =>
      url === '/api/projects/p1/sessions' && init?.method === 'POST'
        ? { status: 201, body: { name: 'my-session', harness: 'opencode' } }
        : undefined)
    vi.stubGlobal('fetch', fetchMock)
    const props = renderDialog()

    fireEvent.change(screen.getByPlaceholderText(/Session name/), { target: { value: 'my-session' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'opencode' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create & Attach' }))
    await waitFor(() => expect(props.onLaunched).toHaveBeenCalledWith('my-session'))
    const body = JSON.parse(String(fetchMock.mock.calls[0][1]?.body))
    expect(body.harnessId).toBe('opencode')
    expect(body.name).toBe('my-session')
  })

  it('only installed harnesses appear in the dropdown', async () => {
    renderDialog({ harnesses: HARNESS_NOT_INSTALLED })
    // shell is always there, but the not-installed harness should be hidden
    expect(screen.getByRole('combobox')).toBeInTheDocument()
    expect(screen.queryByText('OpenCode')).not.toBeInTheDocument()
    expect(screen.getByText('Shell (bash)')).toBeInTheDocument()
  })

  it('name is always required — harness launch without a name stays disabled', async () => {
    renderDialog()
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'opencode' } })
    // name empty -> disabled
    expect(screen.getByRole('button', { name: 'Create & Attach' })).toBeDisabled()
    fireEvent.change(screen.getByPlaceholderText(/Session name/), { target: { value: 'work' } })
    expect(screen.getByRole('button', { name: 'Create & Attach' })).not.toBeDisabled()
  })

  it('a failed launch surfaces the server error inside the dialog', async () => {
    vi.stubGlobal('fetch', mockFetch(() => ({ status: 422, body: { error: 'not a CLI — it looks like it wants a display' } })))
    renderDialog()

    fireEvent.change(screen.getByPlaceholderText(/Session name/), { target: { value: 'oops' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'opencode' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create & Attach' }))
    expect(await screen.findByText(/not a CLI/)).toBeInTheDocument()
  })

  it('submit is disabled while the name is empty', () => {
    renderDialog()
    expect(screen.getByRole('button', { name: 'Create & Attach' })).toBeDisabled()
  })
})

describe('ProjectPicker', () => {
  it('apply is disabled with nothing selected and enabled otherwise', () => {
    const projects = [{ id: 'a', name: 'alpha' }, { id: 'b', name: 'beta' }]
    const base = { projects, onToggle: () => {}, busy: false, onApply: vi.fn(), onCancel: () => {} }

    const { unmount } = render(<ProjectPicker {...base} picked={{ a: false, b: false }} applyLabel="Install in 0 project(s)" />)
    expect(screen.getByRole('button', { name: /Install in 0/ })).toBeDisabled()
    unmount()

    render(<ProjectPicker {...base} picked={{ a: true, b: false }} applyLabel="Install in 1 project(s)" />)
    fireEvent.click(screen.getByRole('button', { name: /Install in 1 project/ }))
    expect(base.onApply).toHaveBeenCalledTimes(1)
  })

  it('installed projects are shown as Installed and cannot be toggled', () => {
    const projects = [{ id: 'a', name: 'alpha' }, { id: 'b', name: 'beta' }]
    const base = {
      projects,
      picked: { a: false, b: true },
      onToggle: vi.fn(),
      busy: false,
      onApply: vi.fn(),
      onCancel: () => {},
      installed: { a: true, b: false },
      applyLabel: 'Install in 1 project(s)',
    }
    render(<ProjectPicker {...base} />)
    expect(screen.getByText('Installed')).toBeInTheDocument()
    const checkboxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(checkboxes[0].disabled).toBe(true) // alpha is installed
    expect(checkboxes[1].disabled).toBe(false)
  })
})

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
