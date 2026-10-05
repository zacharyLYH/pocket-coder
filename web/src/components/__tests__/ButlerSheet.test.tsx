import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ButlerSheet } from '@/components/ButlerSheet'
import { mockFetch } from '@/test/mockFetch'

const THREAD = 'ab12cd34ef56ab78cd90ef13'

function turnBody(answer = 'All healthy.', extra: Record<string, unknown> = {}): string {
  return JSON.stringify({ threadId: THREAD, threadTitle: 'Brief me', turnId: 't1', answer, steps: [], time: '2026-09-02T10:00:00Z', ...extra })
}

function mockAll(body: string, threadAnswer = 'All healthy.') {
  const fetchMock = mockFetch((url, init) => {
    if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
      return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me', status: 'ready' }]} }
    if (url === `/api/butler/threads/${THREAD}`)
      return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: 'ready', approvals: [], turns: [{ turnId: 't1', prompt: 'Brief me', answer: threadAnswer, steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
    return undefined
  })
  const realFetch = vi.fn(async (url: string, init?: RequestInit) => {
    if (String(url) === '/api/butler/turn') return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (String(url).endsWith('/retry')) return new Response(body, { status: 200, headers: { 'Content-Type': 'application/json' } })
    return fetchMock(url, init)
  })
  vi.stubGlobal('fetch', realFetch)
  return realFetch
}

describe('ButlerSheet', () => {
  beforeEach(() => { vi.unstubAllGlobals() })

  it('shows presets, hint chip, and thread list', async () => {
    mockAll(turnBody())
    render(<ButlerSheet projectHint="a/b" onClearHint={vi.fn()} onClose={vi.fn()} />)
    expect(await screen.findByTestId('butler-sheet')).toBeInTheDocument()
    expect(screen.getByTestId('butler-hint')).toHaveTextContent('looking at: a/b')
    expect(screen.getByTestId('butler-preset-Brief me')).toBeInTheDocument()
    expect(await screen.findByTestId(`butler-thread-${THREAD}`)).toBeInTheDocument()
  })

  it('sends a turn and renders the answer', async () => {
    mockAll(turnBody())
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('All healthy.'))
    expect(screen.queryByTestId('butler-pending')).not.toBeInTheDocument()
  })

  it('renders Butler answers as markdown', async () => {
    const answer = '**Healthy**\n\n- Docker\n- SMTP'
    mockAll(turnBody(answer), answer)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    const answerBox = await screen.findByTestId('butler-answer')
    expect(answerBox.querySelector('strong')).toHaveTextContent('Healthy')
    expect(answerBox.querySelectorAll('li')).toHaveLength(2)
  })

  // Async-butler contract: the POST returns as soon as the turn is
  // reserved, so the very next thread GET already carries the user's
  // prompt (status running, no answer yet) — the bubble shows during the
  // run, then polling picks up the answer. No client-side echo exists.
  it('shows the reserved prompt and typing while the in-flight turn runs', async () => {
    let threadGets = 0
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me', status: 'running' }] } }
      if (url === `/api/butler/threads/${THREAD}`) {
        threadGets++
        const done = threadGets >= 2
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: done ? 'ready' : 'running', approvals: [], turns: [{ turnId: 't1', prompt: 'Brief me', answer: done ? 'All healthy.' : undefined, steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      // POST answers immediately with only the reservation identity.
      if (String(url) === '/api/butler/turn') return new Response(JSON.stringify({ threadId: THREAD, threadTitle: 'Brief me', turnId: 't1', time: '2026-09-02T10:00:00Z' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      return fetchMock(String(url), init)
    }))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    // The reserved turn (prompt) is visible while the run is still going.
    const turn = await screen.findByTestId('butler-turn')
    expect(turn).toHaveTextContent('Brief me')
    expect(screen.getByTestId('butler-pending')).toBeInTheDocument()
    expect(screen.queryByTestId('butler-answer')).not.toBeInTheDocument()
    // Poll settles: the answer replaces the typing indicator.
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('All healthy.'), { timeout: 10000 })
  })

  it('shows a pending indicator while the turn is in flight', async () => {
    const realFetch = mockAll('')
    let resolveTurn!: (r: Response) => void
    const gate = new Promise<Response>((res) => { resolveTurn = res })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url) === '/api/butler/turn') return gate
      return realFetch(String(url), init)
    }))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    await waitFor(() => expect(screen.getByTestId('butler-pending')).toBeInTheDocument())
    resolveTurn(new Response(turnBody(), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('All healthy.'))
    await waitFor(() => expect(screen.queryByTestId('butler-pending')).not.toBeInTheDocument())
  })

  it('renders confirm cards with blast radius, Confirm and Discard', async () => {
    const final = turnBody('Ready to stop.', { confirms: [{ id: 'c1', tool: 'stop', summary: 'Stop project a/b?', blastRadius: 'Stops its container and preview. Sessions end.' }] })
    let discarded = false
    const card = { id: 'c1', tool: 'stop', summary: 'Stop project a/b?', blastRadius: 'Stops its container and preview. Sessions end.' }
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: []} }
      if (url === `/api/butler/threads/${THREAD}`)
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: discarded ? 'ready' : 'awaiting', approvals: discarded ? [] : [card], turns: [{ turnId: 't1', prompt: 'Stop it', answer: 'Ready to stop.', steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      if (url === '/api/butler/confirms/c1/discard' && init?.method === 'POST') {
        discarded = true
        return { status: 200, body: { ok: true } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url) === '/api/butler/turn') return new Response(final, { status: 200, headers: { 'Content-Type': 'application/json' } })
      return fetchMock(String(url), init)
    }))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Stop it' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    expect(await screen.findByTestId('butler-confirm')).toBeInTheDocument()
    expect(screen.getByTestId('butler-confirm-blast')).toHaveTextContent('Stops its container')
    expect(screen.getByTestId('butler-approval-block')).toHaveTextContent('Confirm or discard')
    expect(screen.getByTestId('butler-prompt')).toBeDisabled()
    fireEvent.click(screen.getByTestId('butler-confirm-no'))
    await waitFor(() => expect(screen.queryByTestId('butler-confirm')).not.toBeInTheDocument())
  })

  it('hides retry while an approval is pending', async () => {
    const card = { id: 'c1', tool: 'stop', summary: 'Stop project?', blastRadius: 'Container stops.' }
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Stop it', status: 'awaiting' }] } }
      if (url === `/api/butler/threads/${THREAD}`)
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: 'awaiting', approvals: [card], turns: [{ turnId: 't1', prompt: 'Stop it', steps: [], time: '2026-09-02T10:00:00Z', error: 'model blew up' }] } } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(await screen.findByTestId(`butler-thread-${THREAD}`))
    expect(await screen.findByTestId('butler-error')).toHaveTextContent('model blew up')
    expect(screen.queryByTestId('butler-retry')).not.toBeInTheDocument()
    expect(screen.getByTestId('butler-approval-block')).toBeInTheDocument()
  })

  it('clears pending cards when starting a new chat', async () => {
    const final = turnBody('Ready.', { confirms: [{ id: 'c1', tool: 'stop', summary: 'Stop project?', blastRadius: 'Container stops.' }] })
    const card = { id: 'c1', tool: 'stop', summary: 'Stop project?', blastRadius: 'Container stops.' }
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: []} }
      if (url === `/api/butler/threads/${THREAD}`)
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: 'awaiting', approvals: [card], turns: [{ turnId: 't1', prompt: 'Stop it', answer: 'Ready.', steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      return undefined
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url) === '/api/butler/turn') return new Response(final, { status: 200, headers: { 'Content-Type': 'application/json' } })
      return fetchMock(String(url), init)
    }))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Stop it' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    expect(await screen.findByTestId('butler-confirm')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('butler-new'))
    expect(screen.queryByTestId('butler-confirm')).not.toBeInTheDocument()
  })

  it('blocks the composer when the open thread has a run in flight', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me', status: 'running' }]} }
      if (url === `/api/butler/threads/${THREAD}`)
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: 'running', approvals: [], turns: [{ turnId: 't1', prompt: 'Brief me', answer: 'Working…', steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(await screen.findByTestId(`butler-thread-${THREAD}`))
    await waitFor(() => expect(screen.getByTestId('butler-running-block')).toBeInTheDocument())
    expect(screen.getByTestId('butler-pending')).toBeInTheDocument()
    expect(screen.getByTestId('butler-prompt')).toBeDisabled()
    expect(screen.getByTestId('butler-send')).toBeDisabled()
  })

  it('polls an in-flight thread until the answer lands (reload-proof)', async () => {
    let threadGets = 0
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me', status: 'running' }]} }
      if (url === `/api/butler/threads/${THREAD}`) {
        threadGets++
        const done = threadGets >= 2
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', status: done ? 'ready' : 'running', approvals: [], turns: [{ turnId: 't1', prompt: 'Brief me', answer: done ? 'Landed.' : undefined, steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      }
      return undefined
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.click(await screen.findByTestId(`butler-thread-${THREAD}`))
    await screen.findByTestId('butler-turn')
    expect(screen.queryByTestId('butler-answer')).not.toBeInTheDocument()
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('Landed.'), { timeout: 10000 })
  })

  it('clears the hint chip', async () => {
    mockAll(turnBody())
    const onClear = vi.fn()
    render(<ButlerSheet projectHint="a/b" onClearHint={onClear} onClose={vi.fn()} />)
    await screen.findByTestId('butler-hint')
    fireEvent.click(screen.getByTestId('butler-hint-clear'))
    expect(onClear).toHaveBeenCalled()
  })
})
