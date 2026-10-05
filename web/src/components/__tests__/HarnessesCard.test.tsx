import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within, act } from '@testing-library/react'
import { HarnessesCard } from '@/components/HarnessesCard'
import { mockFetch, type FetchCall } from '@/test/mockFetch'

// Unit tests for the per-project harness card. The backend is mocked at the
// fetch level: these tests pin the single-project behavior fast (Install
// hits THIS project directly, no picker) — the real backend path is covered
// by the Playwright suite.

const PROJECT = { id: 'x/alpha' }

const HARNESS_RESPONSE = {
  harnesses: [
    { id: 'terminal', name: 'Terminal', command: 'bash' },
    { id: 'opencode', name: 'OpenCode', command: 'opencode', install: 'npm i -g opencode-ai' },
    { id: 'localtool', name: 'Local Tool', command: 'localtool' },
  ],
}

beforeEach(() => {
  window.confirm = vi.fn(() => true)
})

describe('HarnessesCard', () => {
  it('renders suggestions from the registry, hiding the bash shell', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard project={{ id: 'x/alpha' }} />)

    expect(await screen.findByText('OpenCode')).toBeInTheDocument()
    expect(screen.getByText('npm i -g opencode-ai')).toBeInTheDocument()
    expect(screen.queryByText('Terminal')).not.toBeInTheDocument()
    // no install command → nothing to download
    expect(screen.getByText('no download needed')).toBeInTheDocument()
  })

  it('shows Installed when already installed in this project', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard project={{ id: 'x/alpha', harnesses: ['opencode'] }} />)

    await screen.findByText('OpenCode')
    expect(screen.getByText('Installed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
  })

  it('shows Installed from the live probe even when the record is empty', async () => {
    // Regression: binaries can exist in the container without a state.json
    // record (manual npm install, reused home volume). The per-project probe
    // is live truth — the row must show Installed, not an Install button.
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/x%2Fbeta/harnesses') {
        return {
          status: 200,
          body: { harnesses: [{ id: 'opencode', name: 'OpenCode', command: 'opencode', installed: true }] },
        }
      }
      return undefined
    }))
    render(<HarnessesCard project={{ id: 'x/beta' }} />)

    await screen.findByText('OpenCode')
    expect(screen.getByText('Installed')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
  })

  it('keeps the Install button when this project lacks it', async () => {
    vi.stubGlobal('fetch', mockFetch((url) =>
      url === '/api/harnesses' ? { status: 200, body: HARNESS_RESPONSE } : undefined))
    render(<HarnessesCard project={{ id: 'x/beta' }} />)

    await screen.findByText('OpenCode')
    expect(screen.getByRole('button', { name: 'Install' })).toBeInTheDocument()
    expect(screen.queryByText('Installed')).not.toBeInTheDocument()
  })

  it('installs straight into this project — one click, no picker', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return { status: 200, body: { results: [{ project: 'x/beta', status: 'ok' }] } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard project={{ id: 'x/beta' }} onInstalled={vi.fn()} />)

    // Install hits this project directly — no picker opens.
    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))

    await screen.findByText('Installed.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/install'))
    expect(JSON.parse(String(call![1]?.body)).projectIds).toEqual(['x/beta'])
  })

  it('surfaces install errors as errors, not success', async () => {
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/install') {
        return {
          status: 200,
          body: { results: [{ project: 'x/beta', status: 'error', detail: 'npm ERR! network unreachable' }] },
        }
      }
      return undefined
    }))
    render(<HarnessesCard project={{ id: 'x/beta' }} />)

    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))

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
    render(<HarnessesCard project={PROJECT} />)

    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Add harness' }))
    const dialog = await screen.findByRole('dialog')
    const dialogSubmit = () => within(dialog).getByRole('button', { name: 'Add harness' })

    // server rejection stays in the dialog
    fireEvent.change(within(dialog).getByPlaceholderText('Name (e.g. My Agent)'), { target: { value: 'dup' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Startup command (e.g. my-agent)'), { target: { value: 'dup' } })
    fireEvent.click(dialogSubmit())
    await waitFor(() => expect(screen.getByText('duplicate harness name')).toBeInTheDocument())
    expect(dialog).toBeInTheDocument() // dialog still open

    // a valid add closes the dialog and the new suggestion appears
    fireEvent.change(within(dialog).getByPlaceholderText('Name (e.g. My Agent)'), { target: { value: 'Mine' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Startup command (e.g. my-agent)'), { target: { value: 'mine' } })
    fireEvent.click(dialogSubmit())
    expect(await screen.findByText('Mine')).toBeInTheDocument()
  })

  it('shows an Update button when the check finds a newer version', async () => {
    const fetchMock = mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/harnesses/opencode/update-check') {
        return { status: 200, body: { current: '0.9.0', latest: '0.10.0', updateAvailable: true } }
      }
      if (url === '/api/harnesses/opencode/install') {
        return { status: 200, body: { results: [{ project: 'x/alpha', status: 'ok' }] } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<HarnessesCard project={{ id: 'x/alpha', harnesses: ['opencode'] }} />)

    // badge shows the upgrade path, Update re-installs straight into this project
    expect(await screen.findByTestId('harness-update-badge-opencode')).toHaveTextContent('0.9.0 → 0.10.0')
    fireEvent.click(screen.getByTestId('harness-update-opencode'))

    await screen.findByText('Installed.')
    const call = fetchMock.mock.calls.find(([u]) => String(u).includes('/install'))
    expect(JSON.parse(String(call![1]?.body)).projectIds).toEqual(['x/alpha'])
  })

  it('stays silent when the check is unavailable', async () => {
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url.includes('/update-check')) return { status: 409, body: { error: 'start a project first' } }
      return undefined
    }))
    render(<HarnessesCard project={{ id: 'x/alpha', harnesses: ['opencode'] }} />)

    await screen.findByText('OpenCode')
    await waitFor(() => expect(screen.getByText('Installed')).toBeInTheDocument())
    expect(screen.queryByTestId('harness-update-opencode')).not.toBeInTheDocument()
  })

  it('shows Installing… from the probe when an install is running elsewhere', async () => {
    // The install started in a dialog that has since unmounted (or another
    // browser): no record, binary not yet present, but the server reports
    // installing. The row must show a disabled Installing… — never a second
    // Install button for work already underway.
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/x%2Fbeta/harnesses') {
        return {
          status: 200,
          body: { harnesses: [{ id: 'opencode', name: 'OpenCode', command: 'opencode', installed: false, installing: true }] },
        }
      }
      return undefined
    }))
    render(<HarnessesCard project={{ id: 'x/beta' }} />)

    await screen.findByText('OpenCode')
    const badge = await screen.findByTestId('harness-installing-opencode')
    expect(badge).toHaveTextContent('Installing…')
    expect(badge).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Install' })).not.toBeInTheDocument()
    expect(screen.queryByText('Installed')).not.toBeInTheDocument()
  })

  it('prefers Installed over Installing once the binary lands', async () => {
    // Between the record write and the flag clear the probe reports both;
    // Installed is the truth then.
    vi.stubGlobal('fetch', mockFetch((url) => {
      if (url === '/api/harnesses') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/x%2Fbeta/harnesses') {
        return {
          status: 200,
          body: { harnesses: [{ id: 'opencode', name: 'OpenCode', command: 'opencode', installed: true, installing: true }] },
        }
      }
      return undefined
    }))
    render(<HarnessesCard project={{ id: 'x/beta' }} />)

    await screen.findByText('OpenCode')
    expect(await screen.findByText('Installed')).toBeInTheDocument()
    expect(screen.queryByTestId('harness-installing-opencode')).not.toBeInTheDocument()
  })

  it('fetches once per mount and installs once per click', async () => {
    // Pins the component's own fetch discipline: one GET per endpoint per
    // mount, no refetch on re-render (effect deps), exactly one install POST
    // per click (button disables mid-flight). (StrictMode's dev
    // double-invoke is React's own behavior, not pinned here.)
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      const method = init?.method ?? 'GET'
      if (url === '/api/harnesses' && method === 'GET') return { status: 200, body: HARNESS_RESPONSE }
      if (url === '/api/projects/x%2Fgamma/harnesses' && method === 'GET') {
        return { status: 200, body: { harnesses: [] } }
      }
      if (url === '/api/harnesses/opencode/install' && method === 'POST') {
        return { status: 200, body: { results: [{ project: 'x/gamma', status: 'ok' }] } }
      }
      return undefined
    }, (c) => calls.push(c)))
    const { rerender } = render(
      <HarnessesCard project={{ id: 'x/gamma' }} onInstalled={vi.fn()} />,
    )

    await screen.findByText('OpenCode')
    // Flush any stragglers, then count.
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    const gets = (url: string) => calls.filter((c) => c.url === url && c.method === 'GET').length
    expect(gets('/api/harnesses')).toBe(1)
    expect(gets('/api/projects/x%2Fgamma/harnesses')).toBe(1)

    // Re-rendering with the same project must not refetch (effect deps).
    rerender(<HarnessesCard project={{ id: 'x/gamma' }} onInstalled={vi.fn()} />)
    await act(async () => { await new Promise((r) => setTimeout(r, 50)) })
    expect(gets('/api/harnesses')).toBe(1)
    expect(gets('/api/projects/x%2Fgamma/harnesses')).toBe(1)

    // One click → exactly one install POST (button disables mid-flight).
    fireEvent.click(screen.getByRole('button', { name: 'Install' }))
    await screen.findByText('Installed.')
    expect(calls.filter((c) => c.url === '/api/harnesses/opencode/install' && c.method === 'POST')).toHaveLength(1)
  })

  it('bring-your-own journey: add a custom harness, install it, see Installed', async () => {
    // The full user flow for a custom agent CLI: register it through the
    // dialog (catalog only — nothing downloaded), then Install targets
    // this project directly, and the row flips to Installed.
    let added = false
    const calls: FetchCall[] = []
    vi.stubGlobal('fetch', mockFetch((url, init) => {
      const method = init?.method ?? 'GET'
      if (url === '/api/harnesses' && method === 'GET') {
        return {
          status: 200,
          body: added
            ? { harnesses: [...HARNESS_RESPONSE.harnesses, { id: 'mine', name: 'Mine', command: 'mine', install: 'npm i -g mine' }] }
            : HARNESS_RESPONSE,
        }
      }
      if (url === '/api/harnesses' && method === 'POST') {
        added = true
        return { status: 201, body: { id: 'mine' } }
      }
      if (url === '/api/projects/x%2Falpha/harnesses' && method === 'GET') {
        return { status: 200, body: { harnesses: [] } }
      }
      if (url === '/api/harnesses/mine/install' && method === 'POST') {
        return { status: 200, body: { results: [{ project: 'x/alpha', status: 'ok' }] } }
      }
      return undefined
    }, (c) => calls.push(c)))
    render(<HarnessesCard project={{ id: 'x/alpha' }} onInstalled={vi.fn()} />)

    // Register through the dialog, with an install command this time.
    await screen.findByText('OpenCode')
    fireEvent.click(screen.getByRole('button', { name: 'Add harness' }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByPlaceholderText('Name (e.g. My Agent)'), { target: { value: 'Mine' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Startup command (e.g. my-agent)'), { target: { value: 'mine' } })
    fireEvent.change(within(dialog).getByPlaceholderText('Install command (optional, e.g. npm i -g my-agent)'), { target: { value: 'npm i -g mine' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add harness' }))
    expect(await screen.findByText('Mine')).toBeInTheDocument()

    // The custom row offers Install (OpenCode's row does too — Mine renders
    // last since the registry appends it). Clicking it installs straight
    // into this project and flips the row to Installed.
    const installBtns = screen.getAllByRole('button', { name: 'Install' })
    expect(installBtns).toHaveLength(2)
    fireEvent.click(installBtns[installBtns.length - 1])
    await screen.findByText('Installed.')
    const call = calls.find((c) => c.url === '/api/harnesses/mine/install')
    expect(call?.method).toBe('POST')
    expect(JSON.parse(String(call?.body)).projectIds).toEqual(['x/alpha'])
    expect(await screen.findByTestId('harness-installed-mine')).toBeInTheDocument()
  })
})
