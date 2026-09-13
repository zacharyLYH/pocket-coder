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
  test.use({ viewport: { width: 1280, height: 720 } })

  test('AI card renders on home', async ({ page }) => {
    await page.goto('/')
    await expect(page.getByTestId('ai-card')).toBeVisible()
    await expect(page.getByTestId('ai-test')).toBeVisible()
    await expect(page.getByTestId('ai-save')).toBeDisabled()
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
    await page.route('**/api/projects/*/codemap', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(TURN) })
    })
    await page.route('**/api/projects/*/file*', async (route) => {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(FILE_BODY) })
    })
    await page.goto(terminalUrl(FAKE_ID, 'main'))
    await page.getByTestId('tab-codemap').click()
    await page.getByTestId('codemap-prompt').fill('Where does login happen?')
    await page.getByTestId('codemap-generate').click()
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
    await expect(page.getByText('PIN login lives in the auth package')).toBeVisible()
    await expect(page).toHaveScreenshot('codemap-turn.png')
    await page.getByTestId('codemap-ref').click()
    await expect(page.getByTestId('file-overlay')).toBeVisible()
    await expect(page.getByTestId('file-content')).toContainText('func Verify')
    await expect(page).toHaveScreenshot('codemap-overlay.png')
    await page.getByTestId('file-back').click()
    await expect(page.getByTestId('file-overlay')).toHaveCount(0)
    await expect(page.getByTestId('codemap-turn')).toBeVisible()
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
