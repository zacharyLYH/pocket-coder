import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { HarnessesCard, ProjectPicker } from '@/components/HarnessesCard'
import { mockFetch } from '@/test/mockFetch'

// Unit tests for the harness/command orchestration card. The backend is
// mocked at the fetch level: these tests exist to pin edge-case behavior
// (selective application, error surfacing, empty states) fast — the real
// backend path is covered by the Playwright suite. ProjectPicker, whose
// selection logic lives entirely in service of this card, is tested here too.

const PROJECTS = [{ id: 'x/alpha' }, { id: 'x/beta' }]

const HARNESS_RESPONSE = {
  harnesses: [
    { id: 'terminal', name: 'Terminal', command: 'bash' },
    { id: 'opencode', name: 'OpenCode', command: 'opencode', install: 'npm i -g opencode-ai' },
    { id: 'vi-demo', name: 'Vi Demo', command: 'vi notes.txt' },
  ],
}

beforeEach(() => {
  window.confirm = vi.fn(() => true)
})

describe('HarnessesCard', () => {
  it('renders suggestions from the registry, hiding the bash shell', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard projects={PROJECTS} />)

    expect(await screen.findByText('OpenCode')).toBeInTheDocument()
    expect(screen.getByText('npm i -g opencode-ai')).toBeInTheDocument()
    expect(screen.queryByText('Terminal')).not.toBeInTheDocument()
    // no install command → nothing to download
    expect(screen.getByText('no download needed')).toBeInTheDocument()
  })

  it('shows Installed instead of the button when already everywhere', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard projects={[{ id: 'x/alpha', harnesses: ['opencode'] }]} />)

    await screen.findByText('OpenCode')
    expect(screen.getByText('Installed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install…' })).not.toBeInTheDocument()
  })

  it('keeps the Install button when only some projects have it', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard projects={[{ id: 'x/alpha', harnesses: ['opencode'] }, { id: 'x/beta' }]} />)

    await screen.findByText('OpenCode')
    expect(screen.getByRole('button', { name: 'Install…' })).toBeInTheDocument()
    expect(screen.queryByText('Installed')).not.toBeInTheDocument()
  })

  it('applies an install to exactly the checked projects', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return { status: 200, body: { results: [{ project: 'x/beta', status: 'ok' }] } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard projects={PROJECTS} />)

    // open the picker (both pre-checked), uncheck alpha, apply to beta only
    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Install…' }))
    // picker root: the bordered box that contains the project labels
    const picker = screen.getByText('x/alpha').closest('div.rounded-md')!
    fireEvent.click(picker.querySelectorAll('label input')[0]!)
    fireEvent.click(screen.getByRole('button', { name: /Install in 1 project/ }))

    await screen.findByText('Applied to 1 project.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/install'))
    expect(JSON.parse(String(call![1]?.body)).projectIds).toEqual(['x/beta'])
  })

  it('surfaces per-project install errors as errors, not success', async () => {
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return {
          status: 200,
          body: { results: [{ project: 'x/alpha', status: 'error', detail: 'npm ERR! network unreachable' }] },
        }
      }
      return undefined
    }))
    render(<HarnessesCard projects={PROJECTS} />)

    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Install…' }))
    fireEvent.click(screen.getByRole('button', { name: /Install in 2 project/ }))

    const msg = await screen.findByText(/npm ERR! network unreachable/)
    expect(msg).toHaveClass('text-destructive')
  })

  it('adds a harness through the dialog and surfaces server rejection', async () => {
    let added = false
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/harnesses' && (!init || !init.method || init.method === 'GET')) {
        return added
          ? { status: 200, body: { harnesses: [...HARNESS_RESPONSE.harnesses, { id: 'mine', name: 'Mine', command: 'mine' }] } }
          : { status: 200, body: HARNESS_RESPONSE }
      }
      if (url === '/api/harnesses' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        if (body.name === 'dup') return { status: 400, body: { error: 'duplicate harness name' } }
        added = true
        return { status: 201, body: { id: 'mine' } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard projects={PROJECTS} />)

    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Add harness' }))
    const dialog = await screen.findByRole('dialog')
    const dialogSubmit = () => within(dialog).getByRole('button', { name: 'Add harness' })

    // server rejection stays in the dialog
    fireEvent.change(within(dialog).getByPlaceholderText('Name (e.g. My Agent)'), { target: { value: 'dup' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Command (e.g. my-agent)'), { target: { value: 'dup' } })
    fireEvent.click(dialogSubmit())
    await waitFor(() => expect(screen.getByText('duplicate harness name')).toBeInTheDocument())
    expect(dialog).toBeInTheDocument() // dialog still open

    // a valid add closes the dialog and the new suggestion appears
    fireEvent.change(within(dialog).getByPlaceholderText('Name (e.g. My Agent)'), { target: { value: 'Mine' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Command (e.g. my-agent)'), { target: { value: 'mine' } })
    fireEvent.click(dialogSubmit())
    expect(await screen.findByText('Mine')).toBeInTheDocument()
  })
})

describe('ProjectPicker', () => {
  it('apply is disabled with nothing selected and enabled otherwise', () => {
    const projects = [{ id: 'x/alpha' }, { id: 'x/beta' }]
    const base = { projects, onToggle: () => {}, busy: false, onApply: vi.fn(), onCancel: () => {} }

    const { unmount } = render(<ProjectPicker {...base} picked={{ 'x/alpha': false, 'x/beta': false }} applyLabel="Install in 0 project(s)" />)
    expect(screen.getByRole('button', { name: /Install in 0/ })).toBeDisabled()
    unmount()

    render(<ProjectPicker {...base} picked={{ 'x/alpha': true, 'x/beta': false }} applyLabel="Install in 1 project(s)" />)
    fireEvent.click(screen.getByRole('button', { name: /Install in 1 project/ }))
    expect(base.onApply).toHaveBeenCalledTimes(1)
  })

  it('installed projects are shown as Installed and cannot be toggled', () => {
    const projects = [{ id: 'x/alpha' }, { id: 'x/beta' }]
    const base = {
      projects,
      picked: { 'x/alpha': false, 'x/beta': true },
      onToggle: vi.fn(),
      busy: false,
      onApply: vi.fn(),
      onCancel: () => {},
      installed: { 'x/alpha': true, 'x/beta': false },
      applyLabel: 'Install in 1 project(s)',
    }
    render(<ProjectPicker {...base} />)
    expect(screen.getByText('Installed')).toBeInTheDocument()
    const checkboxes = screen.getAllByRole('checkbox') as HTMLInputElement[]
    expect(checkboxes[0].disabled).toBe(true) // alpha is installed
    expect(checkboxes[1].disabled).toBe(false)
  })
})
