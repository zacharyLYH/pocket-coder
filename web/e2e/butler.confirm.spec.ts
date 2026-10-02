import { expect, test, type Page } from './test'

import { mockProject } from './mocks'
import { mockButlerConfirms, mockButlerThreads, mockButlerTurn } from './threadMocks'

const FAKE_ID = 'e2e/butler-fake'

async function openSheet(page: Page) {
  await mockProject(page, FAKE_ID)
  await page.getByTestId('butler-fab').click()
  await expect(page.getByTestId('butler-sheet')).toBeVisible()
}

test.describe('butler refusal', () => {
  test('code ask shows refusal plus redirect', async ({ page }) => {
    const refusal = 'Sorry, I am firewalled from reading source code by design. For source code related queries, ask your own LLM or CodeMaps.'
    await mockButlerThreads(page, [{ prompt: 'read main.go', answer: refusal }])
    await mockButlerTurn(page, { answer: refusal })
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
    await mockButlerThreads(page)
    await mockButlerTurn(page, { answer: 'Tap Confirm to stop a/b.', confirms: [stopCard] })
    // Server truth after the turn: the card lives on the thread until resolved.
    await page.route(/\/api\/butler\/threads\/[^/?]+$/, async (route) => {
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          thread: {
            id: 'ab12cd34ef56ab78cd90ef13', title: 'chat', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
            status: applied.length > 0 || discarded.length > 0 ? 'ready' : 'awaiting',
            approvals: applied.length > 0 || discarded.length > 0 ? [] : [stopCard],
            turns: [{ turnId: 't0', prompt: 'stop the api project', answer: 'Tap Confirm to stop a/b.', steps: [], error: null, time: '2026-09-02T10:00:00Z' }],
          },
        }),
      })
    })
    await mockButlerConfirms(page, applied, discarded)
    await openSheet(page)
    await page.getByTestId('butler-prompt').fill('stop the api project')
    await page.getByTestId('butler-send').click()

    await expect(page.getByTestId('butler-confirm')).toBeVisible()
    await expect(page.getByTestId('butler-confirm-summary')).toContainText('Stop project a/b?')
    await expect(page.getByTestId('butler-confirm-blast')).toContainText('Stops its container')
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
    await mockButlerThreads(page)
    await mockButlerTurn(page, { answer: 'This would delete the project.', confirms: [deleteCard] })
    await page.route(/\/api\/butler\/threads\/[^/?]+$/, async (route) => {
      await route.fulfill({
        status: 200, contentType: 'application/json',
        body: JSON.stringify({
          thread: {
            id: 'ab12cd34ef56ab78cd90ef13', title: 'chat', createdAt: '2026-09-02T10:00:00Z', updatedAt: '2026-09-02T10:00:00Z',
            status: discarded.length > 0 ? 'ready' : 'awaiting',
            approvals: discarded.length > 0 ? [] : [deleteCard],
            turns: [{ turnId: 't0', prompt: 'delete the api project', answer: 'This would delete the project.', steps: [], error: null, time: '2026-09-02T10:00:00Z' }],
          },
        }),
      })
    })
    await mockButlerConfirms(page, applied, discarded)
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
