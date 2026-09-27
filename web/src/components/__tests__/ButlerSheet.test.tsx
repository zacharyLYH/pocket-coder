import { describe, expect, it, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { ButlerSheet } from '@/components/ButlerSheet'
import { mockFetch } from '@/test/mockFetch'

const THREAD = 'ab12cd34ef56ab78cd90ef13'

// sseTurn builds one streamed turn body: status lines, then the final JSON.
function sseTurn(statuses: string[], answer = 'All healthy.'): string {
  return statuses.map((s) => `data: ${s}\n\n`).join('') +
    `{"threadId":"${THREAD}","threadTitle":"Brief me","turnId":"t1","answer":${JSON.stringify(answer)},"steps":[],"time":"2026-09-02T10:00:00Z"}\n`
}

function mockAll(sseBody: string, threadAnswer = 'All healthy.') {
  const fetchMock = mockFetch((url, init) => {
    if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
      return { status: 200, body: { threads: [{ id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me' }], runningThreadId: null } }
    if (url === `/api/butler/threads/${THREAD}`)
      return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turns: [{ turnId: 't1', prompt: 'Brief me', answer: threadAnswer, steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
    return undefined
  })
  // /api/butler/turn streams SSE: stub fetch directly (text body, not JSON)
  const realFetch = vi.fn(async (url: string, init?: RequestInit) => {
    if (String(url) === '/api/butler/turn') return new Response(sseBody, { status: 200 })
    return fetchMock(url, init)
  })
  vi.stubGlobal('fetch', realFetch)
  return realFetch
}

describe('ButlerSheet', () => {
  beforeEach(() => { vi.unstubAllGlobals() })

  it('shows presets, hint chip, and thread list', async () => {
    mockAll('')
    render(<ButlerSheet projectHint="a/b" onClearHint={vi.fn()} onClose={vi.fn()} />)
    expect(await screen.findByTestId('butler-sheet')).toBeInTheDocument()
    expect(screen.getByTestId('butler-hint')).toHaveTextContent('looking at: a/b')
    expect(screen.getByTestId('butler-preset-Brief me')).toBeInTheDocument()
    expect(await screen.findByTestId(`butler-thread-${THREAD}`)).toBeInTheDocument()
  })

  it('sends a turn, streams status, and renders the answer', async () => {
    mockAll(sseTurn(['{"tool":"model","status":"running"}']))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('All healthy.'))
    expect(screen.queryByTestId('butler-pending')).not.toBeInTheDocument()
  })

  it('renders Butler answers as markdown', async () => {
    const answer = '**Healthy**\n\n- Docker\n- SMTP'
    mockAll(sseTurn([], answer), answer)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Brief me' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    const answerBox = await screen.findByTestId('butler-answer')
    expect(answerBox.querySelector('strong')).toHaveTextContent('Healthy')
    expect(answerBox.querySelectorAll('li')).toHaveLength(2)
  })

  it('shows a pending turn while the stream is in flight', async () => {
    // Same mock surface as mockAll, but the turn fetch is gated so the
    // optimistic pending state is observable mid-flight.
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
    // The stream hasn't resolved: optimistic prompt + pending skeleton show.
    await waitFor(() => expect(screen.getByTestId('butler-pending')).toBeInTheDocument())
    resolveTurn(new Response(
      sseTurn(['{"tool":"model","status":"running"}', '{"tool":"done","status":"answered"}']),
      { status: 200 },
    ))
    await waitFor(() => expect(screen.getByTestId('butler-answer')).toHaveTextContent('All healthy.'))
    await waitFor(() => expect(screen.queryByTestId('butler-pending')).not.toBeInTheDocument())
  })

  it('renders confirm cards with blast radius, Confirm and Discard', async () => {
    const final = `{"threadId":"${THREAD}","threadTitle":"Brief me","turnId":"t1","answer":"Ready to stop.","steps":[],"confirms":[{"id":"c1","tool":"stop","summary":"Stop project a/b?","blastRadius":"Stops its container and preview. Sessions end."}],"time":"2026-09-02T10:00:00Z"}\n`
    const fetchMock = mockFetch((url, init) => {
      if (url === '/api/butler/threads' && (!init?.method || init.method === 'GET'))
        return { status: 200, body: { threads: [], runningThreadId: null } }
      if (url === `/api/butler/threads/${THREAD}`)
        return { status: 200, body: { thread: { id: THREAD, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turns: [{ turnId: 't1', prompt: 'Stop it', answer: 'Ready to stop.', steps: [], time: '2026-09-02T10:00:00Z', error: null }] } } }
      if (url === '/api/butler/confirms/c1/discard' && init?.method === 'POST')
        return { status: 200, body: { ok: true } }
      return undefined
    })
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url) === '/api/butler/turn') return new Response(`data: {"tool":"done","status":"answered"}\n\n` + final, { status: 200 })
      return fetchMock(String(url), init)
    }))
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    await screen.findByTestId('butler-sheet')
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Stop it' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    expect(await screen.findByTestId('butler-confirm')).toBeInTheDocument()
    expect(screen.getByTestId('butler-confirm-blast')).toHaveTextContent('Stops its container')
    expect(screen.getByTestId('butler-confirm-ok')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('butler-confirm-no'))
    await waitFor(() => expect(screen.queryByTestId('butler-confirm')).not.toBeInTheDocument())
  })

  it('clears pending cards when starting a new chat', async () => {
    const final = `{"threadId":"${THREAD}","threadTitle":"Brief me","turnId":"t1","answer":"Ready.","steps":[],"confirms":[{"id":"c1","tool":"stop","summary":"Stop project?","blastRadius":"Container stops."}],"time":"2026-09-02T10:00:00Z"}\n`
    mockAll(final)
    render(<ButlerSheet projectHint={null} onClearHint={vi.fn()} onClose={vi.fn()} />)
    fireEvent.change(screen.getByTestId('butler-prompt'), { target: { value: 'Stop it' } })
    fireEvent.click(screen.getByTestId('butler-send'))
    expect(await screen.findByTestId('butler-confirm')).toBeInTheDocument()
    fireEvent.click(screen.getByTestId('butler-new'))
    expect(screen.queryByTestId('butler-confirm')).not.toBeInTheDocument()
  })

  it('clears the hint chip', async () => {
    mockAll('')
    const onClear = vi.fn()
    render(<ButlerSheet projectHint="a/b" onClearHint={onClear} onClose={vi.fn()} />)
    await screen.findByTestId('butler-hint')
    fireEvent.click(screen.getByTestId('butler-hint-clear'))
    expect(onClear).toHaveBeenCalled()
  })
})
