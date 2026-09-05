import { vi } from 'vitest'

// mockFetch returns a fetch stub that answers the given URL/handler pairs and
// throws on any unexpected request, so tests fail loudly if the component
// starts calling an endpoint the test didn't plan for. An optional onCall
// recorder lets tests assert on every request (url/method/body).
export type FetchHandler = (url: string, init?: RequestInit) => { status: number; body: unknown } | undefined
export type FetchCall = { url: string; method: string; body?: string }

export function mockFetch(handler: FetchHandler, onCall?: (call: FetchCall) => void) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    onCall?.({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })
    const out = handler(url, init)
    if (!out) throw new Error(`unexpected fetch: ${url}`)
    return new Response(JSON.stringify(out.body), { status: out.status })
  })
}