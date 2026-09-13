import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { NewSessionDialog } from '@/components/terminal/NewSessionDialog'
import { mockFetch } from '@/test/mockFetch'

// Unit tests for the terminal session dialog — the pieces with real logic
// (name-required, installed-only filter, launch-timeout error surfacing).
// Fetch is mocked; the real terminal bridge is covered by the Playwright
// stack tests. (ProjectPicker is tested alongside its host HarnessesCard.)

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

    fireEvent.change(screen.getByPlaceholderText(/Tab name/), { target: { value: 'dev' } })
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

    fireEvent.change(screen.getByPlaceholderText(/Tab name/), { target: { value: 'my-session' } })
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
    fireEvent.change(screen.getByPlaceholderText(/Tab name/), { target: { value: 'work' } })
    expect(screen.getByRole('button', { name: 'Create & Attach' })).not.toBeDisabled()
  })

  it('a failed launch surfaces the server error inside the dialog', async () => {
    vi.stubGlobal('fetch', mockFetch(() => ({ status: 422, body: { error: 'not a CLI — it looks like it wants a display' } })))
    renderDialog()

    fireEvent.change(screen.getByPlaceholderText(/Tab name/), { target: { value: 'oops' } })
    fireEvent.change(screen.getByRole('combobox'), { target: { value: 'opencode' } })
    fireEvent.click(screen.getByRole('button', { name: 'Create & Attach' }))
    expect(await screen.findByText(/not a CLI/)).toBeInTheDocument()
  })

  it('submit is disabled while the name is empty', () => {
    renderDialog()
    expect(screen.getByRole('button', { name: 'Create & Attach' })).toBeDisabled()
  })
})
