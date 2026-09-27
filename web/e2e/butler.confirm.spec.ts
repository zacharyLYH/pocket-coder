import { expect, test, type Page } from '@playwright/test'

import { mockProject } from './mocks'

// Butler refusal + confirm flows with the backend mocked at the browser
// edge (same shape as butler.spec.ts): the turn streams one final JSON
// (answer plus optional confirms), and the confirm endpoints record
// apply/discard calls. Covers the cp6 refusal+redirect, the cp7 card
// (summary + blast + Confirm/Discard), and the cp8 delete blast shown
// before any Confirm.
const FAKE_ID = 'e2e/butler-fake'
const TID = 'ab12cd34ef56ab78cd90ef99'

type Final = { answer: string; confirms?: { id: string; tool: string; summary: string; blastRadius: string }[] }

async function mockThreads(page: Page, turns: { prompt: string; answer: string }[]) {
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
            turns: turns.map((t, i) => ({ turnId: `t${i}`, prompt: t.prompt, answer: t.answer, steps: [], error: null, time: '2026-09-02T10:00:00Z' })),
          },
        }),
      })
      return
    }
    await route.fulfill({
      status: 200, contentType: 'application/json',
      body: JSON.stringify({ threads: [], runningThreadId: null }),
    })
  })
}

async function mockTurn(page: Page, final: Final) {
  await page.route('**/api/butler/turn', async (route) => {
    const body =
      'data: {"tool":"done","status":"answered"}\n\n' +
      JSON.stringify({ threadId: TID, threadTitle: 'chat', turnId: 't0', steps: [], time: '2026-09-02T10:00:00Z', ...final }) + '\n'
    await route.fulfill({ status: 200, contentType: 'text/event-stream', body })
  })
}

async function mockConfirms(page: Page, applied: string[], discarded: string[]) {
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

async function openSheet(page: Page) {
  await mockProject(page, FAKE_ID)
  await page.getByTestId('butler-fab').click()
  await expect(page.getByTestId('butler-sheet')).toBeVisible()
}

test.describe('butler refusal', () => {
  test('code ask shows refusal plus redirect', async ({ page }) => {
    const refusal = 'Sorry, I am firewalled from reading source code by design. For source code related queries, ask your own LLM or CodeMaps.'
    await mockThreads(page, [{ prompt: 'read main.go', answer: refusal }])
    await mockTurn(page, { answer: refusal })
    await openSheet(page)
    await page.getByTestId('butler-prompt').fill('read main.go')
    await page.getByTestId('butler-send').click()
    await expect(page.getByTestId('butler-answer')).toContainText('firewalled')
    await expect(page.getByTestId('butler-answer')).toContainText('CodeMaps')
  })
})

test.describe('butler confirm card', () => {
  const stopCard = {
    id: 'c1', tool: 'stop',
    summary: 'Stop project a/b?',
    blastRadius: 'Stops its container and preview. Sessions end.',
  }

  test('card shows blast radius; Confirm applies and reports', async ({ page }) => {
    const applied: string[] = []
    const discarded: string[] = []
    await mockThreads(page, [])
    await mockTurn(page, { answer: 'Tap Confirm to stop a/b.', confirms: [stopCard] })
    await mockConfirms(page, applied, discarded)
    await openSheet(page)
    await page.getByTestId('butler-prompt').fill('stop the api project')
    await page.getByTestId('butler-send').click()

    await expect(page.getByTestId('butler-confirm')).toBeVisible()
    await expect(page.getByTestId('butler-confirm-summary')).toContainText('Stop project a/b?')
    await expect(page.getByTestId('butler-confirm-blast')).toContainText('Stops its container')
    await expect(applied).toHaveLength(0)
    await expect(page).toHaveScreenshot('butler-confirm.png')

    await page.getByTestId('butler-confirm-ok').click()
    await expect(page.getByTestId('butler-applied')).toContainText('Stopped.')
    await expect(page.getByTestId('butler-confirm')).toHaveCount(0)
    expect(applied).toEqual(['c1'])
    expect(discarded).toEqual([])
  })

  test('delete blast shows before Confirm; Discard runs nothing', async ({ page }) => {
    const applied: string[] = []
    const discarded: string[] = []
    const deleteCard = {
      id: 'c2', tool: 'delete_project',
      summary: 'Delete project a/b?',
      blastRadius: 'This removes the container, its 3 sessions, both volumes, and the project record.',
    }
    await mockThreads(page, [])
    await mockTurn(page, { answer: 'This would delete the project.', confirms: [deleteCard] })
    await mockConfirms(page, applied, discarded)
    await openSheet(page)
    await page.getByTestId('butler-prompt').fill('delete the api project')
    await page.getByTestId('butler-send').click()

    await expect(page.getByTestId('butler-confirm-blast')).toContainText('3 sessions')
    await expect(page.getByTestId('butler-confirm-blast')).toContainText('both volumes')
    expect(applied).toEqual([])

    await page.getByTestId('butler-confirm-no').click()
    await expect(page.getByTestId('butler-confirm')).toHaveCount(0)
    expect(applied).toEqual([])
    expect(discarded).toEqual(['c2'])
  })
})
