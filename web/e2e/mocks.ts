// Browser-edge mocks shared by specs that run on a fake project id: the
// session endpoints must answer so the terminal pane never dials, and the
// ai/models list must show a configured model so gated tabs render.
import type { Page } from '@playwright/test'

import { terminalUrl } from './helpers'

// mockSessions keeps the terminal pane quiet on a project that does not
// exist: list + ensure succeed, the WS dial then dies silently (no error
// banner), and the tab strip renders normally.
export async function mockSessions(page: Page) {
  await page.route('**/api/projects/*/sessions', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ name: 'main' }) })
    } else {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sessions: [{ name: 'main' }] }) })
    }
  })
}

// mockConfigured serves one fake AI model so "ai configured" gates open.
export async function mockConfigured(page: Page) {
  await page.route('**/api/ai/models', async (route) => {
    await route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ models: [{ id: 'm1', label: 'test', baseURL: 'https://api.openai.com/v1', model: 'gpt-4o', hasKey: true }] }),
    })
  })
}

// mockProject lands the page on a fake project's terminal with the session
// and model mocks already in place (the boilerplate most route-mocked specs
// share before their feature-specific mocks).
export async function mockProject(page: Page, id: string, session = 'main') {
  await mockSessions(page)
  await mockConfigured(page)
  await page.goto(terminalUrl(id, session))
}
