import type { Page } from '@playwright/test'

// Shared browser-edge mocks for thread specs. All turns are plain JSON:
// POST returns the final object directly, no SSE framing.
export const TID = 'ab12cd34ef56ab78cd90ef13'

export async function mockButlerThreads(page: Page, turns: { prompt: string; answer: string }[] = []) {
  await page.route(/\/api\/butler\/threads(\/.*)?$/, async (route) => {
    const url = route.request().url()
    if (route.request().method() === 'DELETE') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deleted: true }) })
      return
    }
    if (/\/threads\/[^/?]+$/.test(url)) {
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          thread: {
            id: TID, title: 'chat', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
            status: 'ready', approvals: [],
            turns: turns.map((t, i) => ({ turnId: `t${i}`, prompt: t.prompt, answer: t.answer, steps: [], error: null, time: '2026-09-02T10:00:00Z' })),
          },
        }),
      })
      return
    }
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ threads: []}),
    })
  })
}

export async function mockButlerTurn(page: Page, final: Record<string, unknown>) {
  await page.route('**/api/butler/turn', async (route) => {
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ threadId: TID, threadTitle: 'chat', turnId: 't0', steps: [], time: '2026-09-02T10:00:00Z', ...final }) })
  })
}

export async function mockButlerConfirms(page: Page, applied: string[], discarded: string[]) {
  await page.route(/\/api\/butler\/confirms\/.+$/, async (route) => {
    const url = route.request().url()
    const id = url.split('/api/butler/confirms/')[1].split('/')[0]
    if (url.endsWith('/apply')) {
      applied.push(id)
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true, tool: 'stop', result: 'Stopped.' }) })
      return
    }
    discarded.push(id)
    await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ok: true }) })
  })
}

