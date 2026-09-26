import { expect, test, type Page } from '@playwright/test'

import { mockProject } from './mocks'

// Butler e2e with the model mocked at the browser edge (same shape as
// codemap.spec.ts): the project id is fake, sessions + ai/models +
// butler routes are mocked, SSE turn body streams status then final JSON.
const FAKE_ID = 'e2e/butler-fake'
const TID = 'ab12cd34ef56ab78cd90ef13'

async function mockButler(page: Page, opts?: { answer?: string; turnDelayMs?: number }) {
  const answer = opts?.answer ?? 'All three projects are healthy.'
  const delay = opts?.turnDelayMs ?? 0
  // Regex, not a trailing glob: `threads*` never crosses the `/` before
  // a thread id (same gotcha as codemap.spec.ts) — one handler for list,
  // detail, and delete.
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
            id: TID, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
            turns: [{ turnId: 't1', prompt: 'Brief me', answer, steps: [], projectHint: 'e2e/butler-fake', error: null, time: '2026-09-02T10:00:00Z' }],
          },
        }),
      })
      return
    }
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ threads: [{ id: TID, title: 'Brief me', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z', turnCount: 1, preview: 'Brief me' }], runningThreadId: null }),
    })
  })
  await page.route('**/api/butler/turn', async (route) => {
    // Optional hold so the pending skeleton is observable: pins the
    // streaming half (status/skeleton under the pending turn) while the
    // mock still stands in for the LLM call only.
    if (delay) await new Promise((r) => setTimeout(r, delay))
    const body =
      'data: {"tool":"model","status":"running"}\n\n' +
      JSON.stringify({ threadId: TID, threadTitle: 'Brief me', turnId: 't1', answer, steps: [], time: '2026-09-02T10:00:00Z' }) + '\n'
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body })
  })
}

test.describe('butler desktop', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('fab opens sheet with presets on home', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('butler-fab')).toBeVisible()
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await expect(page.getByTestId('butler-preset-Brief me')).toBeVisible()
    await expect(page.getByTestId('butler-prompt')).toBeVisible()
    await expect(page).toHaveScreenshot('butler-sheet.png')
  })

  test('clicking outside closes the popup on desktop', async ({ page }) => {
    await page.goto('/')
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await page.mouse.click(20, 20)
    await expect(page.getByTestId('butler-sheet')).toHaveCount(0)
  })

  test('project hint chip shows and clears', async ({ page }) => {
    await mockProject(page, FAKE_ID)
    await mockButler(page)
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-hint')).toContainText(`looking at: ${FAKE_ID}`)
    await page.getByTestId('butler-hint-clear').click()
    await expect(page.getByTestId('butler-hint')).toHaveCount(0)
  })

  test('turn round-trip renders answer', async ({ page }) => {
    await mockProject(page, FAKE_ID)
    await mockButler(page, { answer: 'All three projects are healthy.', turnDelayMs: 800 })
    await page.getByTestId('butler-fab').click()
    await page.getByTestId('butler-prompt').fill('Brief me')
    await page.getByTestId('butler-send').click()
    await expect(page.getByTestId('butler-pending')).toBeVisible()
    await expect(page.getByTestId('butler-answer')).toContainText('All three projects are healthy.')
    await expect(page).toHaveScreenshot('butler-answer.png')
  })
})

test.describe('butler phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('sheet opens bottom-anchored on phone', async ({ page }) => {
    await page.goto('/')
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await expect(page.getByTestId('butler-prompt')).toBeVisible()
    await expect(page).toHaveScreenshot('butler-sheet-phone.png')
  })
})
