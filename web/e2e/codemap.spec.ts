import { expect, test, type Page } from '@playwright/test'

import { terminalUrl } from './helpers'

// Codemap e2e against the real backend with the model mocked at the
// browser edge: no engine, no API key, no network model needed.
//
// The project id is fake on purpose — session endpoints are route-mocked
// so the terminal pane never dials, and the codemap APIs are route-mocked
// for the model half. The no-key test uses the REAL /api/ai/config (the
// seed state carries no key), proving key-less backends hide the tab.
const FAKE_ID = 'e2e/codemap-fake'

const TURN = {
  turnId: 't1',
  sha: 'abc123',
  sections: [
    {
      title: 'Auth flow',
      summary: 'PIN login lives in the auth package and hands out a JWT.',
      refs: [{ path: 'server/internal/auth/auth.go', startLine: 10, endLine: 12, snippet: 'func Verify(pin string) bool {' }],
    },
  ],
}

const FILE_BODY = {
  path: 'server/internal/auth/auth.go',
  content: 'line8\nline9\nfunc Verify(pin string) bool {\nline11\nline12\n',
  binary: false,
  moved: false,
  sha: 'abc123',
}

// mockSessions keeps the terminal pane quiet on a project that does not
// exist: list + ensure succeed, the WS dial then dies silently (no error
// banner), and the tab strip renders normally.
async function mockSessions(page: Page) {
  await page.route('**/api/projects/*/sessions', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ name: 'main' }) })
    } else {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sessions: [{ name: 'main' }] }) })
    }
  })
}

async function mockConfigured(page: Page) {
  await page.route('**/api/ai/config', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', configured: true }),
    })
  })
}

test.describe('codemap desktop', () => {
  // UTC pins the locale timestamps in the thread list/turns so baselines
  // are identical on every runner timezone.
  test.use({ viewport: { width: 1280, height: 720 }, timezoneId: 'UTC' })

  test('AI card renders on home', async ({ page }) => {
    await page.goto('/')
    await page.getByTestId('setup-ai').click()
    const dialog = page.getByRole('dialog')
    await expect(dialog.getByTestId('ai-card')).toBeVisible()
    await expect(dialog.getByTestId('ai-test')).toBeVisible()
    await expect(dialog.getByTestId('ai-save')).toBeDisabled()
    await expect(page).toHaveScreenshot('codemap-ai-card.png', { fullPage: true })
  })

  test('codemap tab hidden without a key', async ({ page }) => {
    await mockSessions(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    // real /api/ai/config: the seed state has no key
    await expect(page.getByTestId('tab-diff')).toBeVisible()
    await expect(page.getByTestId('tab-codemap')).toHaveCount(0)
    await expect(page).toHaveScreenshot('codemap-tab-hidden.png')
  })

  test('codemap prompt renders with a key', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await expect(page.getByTestId('tab-codemap')).toBeVisible()
    await page.getByTestId('tab-codemap').click()
    await expect(page.getByTestId('codemap-prompt')).toBeVisible()
    await expect(page.getByText('Ask about this codebase')).toBeVisible()
    await expect(page).toHaveScreenshot('codemap-prompt.png')
  })

  test('mocked one-turn reply and file overlay', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    // v2 implicit flow (all 200s, no POST-threads create): the list is
    // empty, POST /codemap returns the turn, then the tab reloads +
    // opens the thread from the server.
    const THREAD = {
      thread: {
        id: 'c1', project: FAKE_ID, title: 'Where does login happen?',
        createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T12:00:00Z',
        turns: [
          {
            turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
            sections: TURN.sections, tools: [{ tool: 'search_code', args: '{"pattern":"login"}' }],
            error: null, time: '2026-09-02T12:00:00Z',
          },
        ],
      },
    }
    await page.route('**/api/projects/*/codemap/threads', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ threads: [], runningThreadId: null }) })
    })
    await page.route(/\/api\/projects\/.*\/codemap\/threads\/.+$/, async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(THREAD) })
    })
    await page.route('**/api/projects/*/codemap', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...TURN, threadId: 'c1', threadTitle: 'Where does login happen?', time: '2026-09-02T12:00:00Z' }) })
    })
    await page.route('**/api/projects/*/file*', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(FILE_BODY) })
    })
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    // Past chats live in the history drawer, discoverable from the pane.
    await expect(page.getByTestId('codemap-history-toggle')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-list')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await page.getByTestId('codemap-prompt').fill('Where does login happen?')
    await page.getByTestId('codemap-generate').click()
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await expect(page.getByText('PIN login lives in the auth package')).toBeVisible()
    await expect(page.getByTestId('codemap-new-chat')).toBeVisible()
    await expect(page).toHaveScreenshot('codemap-turn.png')
    await page.getByTestId('codemap-ref').click()
    await expect(page.getByTestId('file-overlay')).toBeVisible()
    await expect(page.getByTestId('file-content')).toContainText('func Verify')
    await expect(page).toHaveScreenshot('codemap-overlay.png')
    await page.getByTestId('file-back').click()
    await expect(page.getByTestId('file-overlay')).toHaveCount(0)
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
  })

  // Two previous chats on the server: the drawer lists them newest-first
  // and reopening one renders its stored turns. Times are fixed UTC
  // strings; the describe-level timezoneId keeps them stable per runner.
  const THREADS = [
    { id: 'c1', title: 'Where does login happen?', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T12:00:00Z', turnCount: 1, preview: 'Where does login happen?' },
    { id: 'c2', title: 'Map this repo', createdAt: '2026-08-31T12:00:00Z', updatedAt: '2026-08-31T14:00:00Z', turnCount: 2, preview: 'How does data flow?' },
  ]

  const THREAD_BODIES: Record<string, unknown> = {
    c1: {
      thread: {
        id: 'c1', project: FAKE_ID, title: 'Where does login happen?',
        createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T12:00:00Z',
        turns: [
          {
            turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
            sections: TURN.sections, tools: [{ tool: 'search_code', args: '{"pattern":"login"}' }],
            time: '2026-09-02T12:00:00Z',
          },
        ],
      },
    },
    c2: {
      thread: {
        id: 'c2', project: FAKE_ID, title: 'Map this repo',
        createdAt: '2026-08-31T12:00:00Z', updatedAt: '2026-08-31T14:00:00Z',
        turns: [
          {
            turnId: 't0a', sha: 'abc123', prompt: 'Map this repo',
            sections: [
              { title: 'Entrypoints', summary: 'main.go boots the server and mounts the API.', refs: [] },
              { title: 'Data flow', summary: 'Handlers call services, services hit docker.', refs: [] },
            ],
            tools: [{ tool: 'search_code', args: '{"pattern":"func main"}' }],
            time: '2026-08-31T13:00:00Z',
          },
          {
            turnId: 't0b', sha: 'abc123', prompt: 'How does data flow?',
            sections: [{ title: 'Data flow', summary: 'Requests flow through the mux into services.', refs: [] }],
            tools: [],
            time: '2026-08-31T14:00:00Z',
          },
        ],
      },
    },
  }

  async function mockThreads(page: Page, deleted = new Set<string>()) {
    // Regex: one handler for list, detail, and delete — a trailing glob
    // `threads*` would not cross the `/` before a thread id. No create
    // route in v2 (implicit flow, all 200s); the list carries
    // runningThreadId (null when idle).
    await page.route(/\/api\/projects\/.*\/codemap\/threads(\/.*)?$/, async (route) => {
      const url = route.request().url()
      const method = route.request().method()
      const detail = url.match(/\/codemap\/threads\/([^/?]+)/)?.[1]
      if (method === 'DELETE' && detail) {
        deleted.add(detail)
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ deleted: true }) })
      } else if (detail && THREAD_BODIES[detail] && !deleted.has(detail)) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(THREAD_BODIES[detail]) })
      } else if (detail) {
        await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'unknown thread' }) })
      } else {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ threads: THREADS.filter((t) => !deleted.has(t.id)), runningThreadId: null }) })
      }
    })
  }

  test('previous chats list renders', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    await mockThreads(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    // Mount auto-opens the newest chat; the drawer lists every chat.
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-list')).toBeVisible()
    await expect(page.getByTestId('codemap-thread-item')).toHaveCount(2)
    await expect(page.getByTestId('codemap-thread-list')).toContainText('Where does login happen?')
    await expect(page.getByTestId('codemap-thread-list')).toContainText('Map this repo')
    await expect(page).toHaveScreenshot('codemap-threads.png')
  })

  test('reopened previous chat renders its turns', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    await mockThreads(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-item')).toHaveCount(2)
    await page.getByRole('menuitem', { name: /Map this repo/ }).click()
    await expect(page.getByText('Requests flow through the mux into services.')).toBeVisible()
    await expect(page.getByTestId('codemap-turn')).toHaveCount(2)
    await expect(page.getByTestId('codemap-steps-toggle')).toBeVisible()
    await page.getByTestId('codemap-steps-toggle').click()
    await expect(page.getByTestId('codemap-steps')).toContainText('search_code')
    await expect(page).toHaveScreenshot('codemap-thread-open.png')
  })

  test('delete chat removes it from the menu', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    await mockThreads(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    // Newest chat auto-opens; delete it from the menu.
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-item')).toHaveCount(2)
    await page.locator('[data-thread-id="c1"]').hover()
    await page.locator('[data-thread-id="c1"] [data-testid="codemap-thread-delete"]').click()
    // Deleting the active chat lands back on the empty state.
    await expect(page.getByText('Ask about this codebase')).toBeVisible()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-item')).toHaveCount(1)
    await expect(page.getByTestId('codemap-thread-list')).toContainText('Map this repo')
  })

  // REQUIRED: remount-into-run (route-mocked, no engine/model). Pins the
  // §8 contract: busy restore via runningThreadId, block scope
  // (composer + delete + retry), crash-vs-in-flight, mount-poll only.
  test('remount into running thread shows spinner and blocks', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    const TID = 'ab12cd34ef56ab78cd90ef12'
    const SUMMARY = {
      id: TID, title: 'Where does login happen?',
      createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
      turnCount: 1, preview: 'Where does login happen?',
    }
    const placeholder = {
      turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
      sections: null, tools: null, error: null, time: '2026-09-02T10:00:00Z',
    }
    const filled = {
      turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
      sections: TURN.sections, tools: [{ tool: 'search_code', args: '{"pattern":"login"}' }],
      error: null, time: '2026-09-02T12:00:00Z',
    }
    let running: string | null = TID
    let turn: unknown = placeholder
    await page.route(/\/api\/projects\/.*\/codemap\/threads(\/.*)?$/, async (route) => {
      const url = route.request().url()
      const detail = url.match(/\/codemap\/threads\/([^/?]+)/)?.[1]
      if (detail) {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            thread: {
              id: TID, project: FAKE_ID, title: 'Where does login happen?',
              createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T12:00:00Z', turns: [turn],
            },
          }),
        })
      } else {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ threads: [SUMMARY], runningThreadId: running }),
        })
      }
    })
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    // Mount opened the running thread: spinner on the answer-less
    // placeholder, composer + delete + retry disabled.
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await expect(page.getByTestId('codemap-spinner')).toBeVisible()
    await expect(page.getByTestId('codemap-prompt')).toBeDisabled()
    await expect(page.getByTestId('codemap-generate')).toBeDisabled()
    await expect(page.getByTestId('codemap-retry')).toBeDisabled()
    await page.getByTestId('codemap-history-toggle').click()
    await expect(page.getByTestId('codemap-thread-delete')).toBeDisabled()
    await page.keyboard.press('Escape')
    // Completion lands: re-mock with filled sections, revisit via
    // remount — spinner gone, sections render, controls re-enable.
    turn = filled
    running = null
    await page.reload()
    await page.getByTestId('tab-codemap').click()
    await expect(page.getByTestId('codemap-spinner')).toHaveCount(0)
    await expect(page.getByText('PIN login lives in the auth package')).toBeVisible()
    await expect(page.getByTestId('codemap-prompt')).toBeEnabled()
    await expect(page.getByTestId('codemap-retry')).toHaveCount(0)
  })

  // Remounted runs resolve without a refresh: the tab polls the open
  // thread while busy-without-own-request and adopts the finished turn.
  test('remounted run completes without reload', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    const TID = 'ab12cd34ef56ab78cd90ef14'
    const SUMMARY = {
      id: TID, title: 'Where does login happen?',
      createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
      turnCount: 1, preview: 'Where does login happen?',
    }
    const placeholder = {
      turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
      sections: null, tools: null, error: null, time: '2026-09-02T10:00:00Z',
    }
    const filled = {
      turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
      sections: TURN.sections, tools: [{ tool: 'search_code', args: '{"pattern":"login"}' }],
      error: null, time: '2026-09-02T12:00:00Z',
    }
    let running: string | null = TID
    let turn: unknown = placeholder
    await page.route(/\/api\/projects\/.*\/codemap\/threads(\/.*)?$/, async (route) => {
      const url = route.request().url()
      const detail = url.match(/\/codemap\/threads\/([^/?]+)/)?.[1]
      if (detail) {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            thread: {
              id: TID, project: FAKE_ID, title: 'Where does login happen?',
              createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T12:00:00Z', turns: [turn],
            },
          }),
        })
      } else {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ threads: [SUMMARY], runningThreadId: running }),
        })
      }
    })
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    await expect(page.getByTestId('codemap-spinner')).toBeVisible()
    // The run finishes elsewhere: no reload, the tab picks it up.
    turn = filled
    running = null
    await expect(page.getByTestId('codemap-spinner')).toHaveCount(0, { timeout: 20000 })
    await expect(page.getByText('PIN login lives in the auth package')).toBeVisible({ timeout: 20000 })
    await expect(page.getByTestId('codemap-prompt')).toBeEnabled({ timeout: 20000 })
  })

  test('crashed placeholder renders failed with enabled retry', async ({ page }) => {    await mockSessions(page)
    await mockConfigured(page)
    const TID = 'ab12cd34ef56ab78cd90ef13'
    const SUMMARY = {
      id: TID, title: 'Where does login happen?',
      createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
      turnCount: 1, preview: 'Where does login happen?',
    }
    // Retry reruns the crashed turn: handled inside the same threads
    // route (one handler avoids route-precedence fragility).
    let retried = false
    await page.route(/\/api\/projects\/.*\/codemap\/threads(\/.*)?$/, async (route) => {
      const url = route.request().url()
      if (/\/retry$/.test(url)) {
        retried = true
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ turnId: 't1', threadId: TID, threadTitle: 'Where does login happen?', time: '2026-09-02T12:00:00Z' }),
        })
        return
      }
      const detail = url.match(/\/codemap\/threads\/([^/?]+)/)?.[1]
      if (detail) {
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({
            thread: {
              id: TID, project: FAKE_ID, title: 'Where does login happen?',
              createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
              turns: [{
                turnId: 't1', sha: 'abc123', prompt: 'Where does login happen?',
                sections: null, tools: null, error: null, time: '2026-09-02T10:00:00Z',
              }],
            },
          }),
        })
      } else {
        // Crash variant: runningThreadId null with an answer-less
        // placeholder left behind (e.g. across a server restart).
        await route.fulfill({
          status: 200, contentType: 'application/json',
          body: JSON.stringify({ threads: [SUMMARY], runningThreadId: null }),
        })
      }
    })
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    // Failed/crashed with retry hint — NOT a spinner — retry enabled.
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await expect(page.getByTestId('codemap-spinner')).toHaveCount(0)
    await expect(page.getByTestId('codemap-failed-hint')).toBeVisible()
    await expect(page.getByTestId('codemap-retry')).toBeEnabled()
    await expect(page.getByTestId('codemap-prompt')).toBeEnabled()
    await page.getByTestId('codemap-retry').click()
    await expect.poll(() => retried).toBe(true)
  })
})

test.describe('codemap phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('codemap prompt renders on phone', async ({ page }) => {
    await mockSessions(page)
    await mockConfigured(page)
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    await expect(page.getByTestId('codemap-prompt')).toBeVisible()
    await expect(page).toHaveScreenshot('codemap-prompt-phone.png', { fullPage: true })
  })
})
