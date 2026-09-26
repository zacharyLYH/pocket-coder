import { describe, expect, it, vi, afterEach } from 'vitest'
import { postTurnStream, isFinalShape } from '@/lib/turnStream'

afterEach(() => { vi.unstubAllGlobals() })

type Final = { threadId: string; answer: string }
const isFinal = isFinalShape

function mockText(status: number, body: string) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status })))
}

describe('postTurnStream', () => {
  it('emits each status in order and resolves the final', async () => {
    mockText(200, 'data: {"tool":"model","status":"running"}\n\ndata: {"tool":"done","status":"answered"}\n\n{"threadId":"t","answer":"hi"}\n')
    const seen: string[] = []
    const out = await postTurnStream<Final>('/api/x', {}, isFinal, (s) => { seen.push(`${s.tool}:${s.status}`) })
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
    expect(seen).toEqual(['model:running', 'done:answered'])
  })

  it('throws the server error message on failure bodies', async () => {
    mockText(502, '{"error":"no choices","threadId":"t"}\n')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('no choices')
  })

  it('rejects when the final line is missing', async () => {
    mockText(200, 'data: {"tool":"model","status":"running"}\n\n')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('HTTP 200')
  })

  // --- SSE edge cases ---

  it('ignores blank lines and \r\n framing from other SSE impls', async () => {
    mockText(200, '\r\ndata: {"tool":"model","status":"running"}\r\n\r\n\r\n{"threadId":"t","answer":"hi"}\r\n')
    const seen: string[] = []
    const out = await postTurnStream<Final>('/api/x', {}, isFinal, (s) => { seen.push(s.tool) })
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
    expect(seen).toEqual(['model'])
  })

  it('survives garbage status lines without breaking the stream', async () => {
    mockText(200, 'data: not-json\n\ndata: {"tool":"done","status":"answered"}\n\n{"threadId":"t","answer":"hi"}\n')
    const seen: string[] = []
    const out = await postTurnStream<Final>('/api/x', {}, isFinal, (s) => { seen.push(s.tool) })
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
    expect(seen).toEqual(['done'])
  })

  it('ignores non-JSON junk lines between statuses and the final', async () => {
    mockText(200, ': heartbeat\n\ndata: {"tool":"model","status":"running"}\n\n<!-- -->\n{"threadId":"t","answer":"hi"}\n')
    const out = await postTurnStream<Final>('/api/x', {}, isFinal)
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
  })

  it('zero statuses is valid: only the final line', async () => {
    mockText(200, '{"threadId":"t","answer":"hi"}\n')
    const seen: unknown[] = []
    const out = await postTurnStream<Final>('/api/x', {}, isFinal, (s) => { seen.push(s) })
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
    expect(seen).toEqual([])
  })

  it('keeps the LAST final when several parse as finals', async () => {
    mockText(200, '{"threadId":"a","answer":"stale"}\n{"threadId":"b","answer":"fresh"}\n')
    const out = await postTurnStream<Final>('/api/x', {}, isFinal)
    expect(out).toEqual({ threadId: 'b', answer: 'fresh' })
  })

  it('a final-shape line behind data: still counts as the final, not a status', async () => {
    mockText(200, 'data: {"threadId":"t","answer":"hi"}\n')
    const seen: unknown[] = []
    const out = await postTurnStream<Final>('/api/x', {}, isFinal, (s) => { seen.push(s) })
    expect(out).toEqual({ threadId: 't', answer: 'hi' })
    expect(seen).toEqual([])
  })

  it('an error object behind data: rejects with that message', async () => {
    mockText(200, 'data: {"tool":"model","status":"running"}\n\ndata: {"error":"boom","threadId":"t"}\n')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('boom')
  })

  it('an error body on an otherwise-200 stream rejects (final + ok are both required)', async () => {
    mockText(200, 'data: {"tool":"model","status":"running"}\n\n{"error":"late failure","threadId":"t"}\n')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('late failure')
  })

  it('survives truncated JSON (connection cut mid-line) and reports the missing final', async () => {
    mockText(200, 'data: {"tool":"model","status":"runn')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('HTTP 200')
  })

  it('preserves empty-string answer finals (final shape, not an error)', async () => {
    mockText(200, '{"threadId":"t","answer":""}\n')
    const out = await postTurnStream<Final>('/api/x', {}, isFinal)
    expect(out).toEqual({ threadId: 't', answer: '' })
  })

  it('5xx with a JSON error body rejects with the server message', async () => {
    mockText(500, '{"error":"internal"}')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('internal')
  })

  it('5xx with a non-JSON body rejects with the status line', async () => {
    mockText(503, 'upstream connect error')
    await expect(postTurnStream<Final>('/api/x', {}, isFinal)).rejects.toThrow('HTTP 503')
  })
})
