import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import { HarnessesCard } from '@/components/HarnessesCard'

// Unit tests for the harness/command orchestration card. The backend is
// mocked at the fetch level: these tests exist to pin edge-case behavior
// (selective application, error surfacing, empty states) fast — the real
// backend path is covered by the Playwright suite.

const PROJECTS = [
  { id: 'p1', name: 'alpha' },
  { id: 'p2', name: 'beta' },
]

const HARNESS_RESPONSE = {
  harnesses: [
    { id: 'terminal', name: 'Terminal', command: 'bash' },
    { id: 'opencode', name: 'OpenCode', command: 'opencode', install: 'npm i -g opencode-ai' },
    { id: 'vi-demo', name: 'Vi Demo', command: 'vi notes.txt' },
  ],
}

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

  it('applies an install to exactly the checked projects', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return { status: 200, body: { results: [{ project: 'beta', status: 'ok' }] } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard projects={PROJECTS} />)

    // open the picker (both pre-checked), uncheck alpha, apply to beta only
    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Install…' }))
    // picker root: the bordered box that contains the project labels
    const picker = screen.getByText('alpha').closest('div.rounded-md')!
    fireEvent.click(picker.querySelectorAll('label input')[0]!)
    fireEvent.click(screen.getByRole('button', { name: /Install in 1 project/ }))

    await screen.findByText('Applied to 1 project.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/install'))
    expect(JSON.parse(String(call![1]?.body)).projectIds).toEqual(['p2'])
  })

  it('surfaces per-project install errors as errors, not success', async () => {
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return {
          status: 200,
          body: { results: [{ project: 'alpha', status: 'error', detail: 'npm ERR! network unreachable' }] },
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

  it('refuses to run a command with an empty selection', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/exec') return { status: 200, body: { results: [] } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard projects={PROJECTS} />)

    await screen.findByText('OpenCode')
    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'echo hi' } })
    fireEvent.click(screen.getByRole('button', { name: 'Choose projects…' }))
    // uncheck everything
    const picker = screen.getByText('alpha').closest('div.rounded-md')!
    for (const box of picker.querySelectorAll('label input')) fireEvent.click(box)
    const apply = screen.getByRole('button', { name: /Run in 0 project/ })
    expect(apply).toBeDisabled()
  })

  it('runs an arbitrary command in the selected projects and shows the outcome', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/exec') {
        return {
          status: 200,
          body: {
            results: [
              { project: 'alpha', status: 'ok', detail: 'added 1 package' },
              { project: 'beta', status: 'skipped', detail: 'container not running' },
            ],
          },
        }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard projects={PROJECTS} />)

    await screen.findByText('OpenCode')
    fireEvent.change(screen.getByPlaceholderText(/npm i -g opencode-ai@latest/), { target: { value: 'npm i -g x' } })
    fireEvent.click(screen.getByRole('button', { name: 'Choose projects…' }))
    fireEvent.click(screen.getByRole('button', { name: /Run in 2 project/ }))

    await screen.findByText(/Applied to 1 project \(skipped 1 stopped\)/)
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/api/projects/exec'))
    expect(JSON.parse(String(call![1]?.body))).toEqual({ projectIds: ['p1', 'p2'], command: 'npm i -g x' })
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
