import { expect, test } from '@playwright/test'

import { mockConfigured, mockProject } from './mocks'
import { TID, mockButlerThreads, mockButlerTurn } from './threadMocks'

const FAKE_ID = 'e2e/butler-fake'

test.describe('butler desktop', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('fab hidden without a key', async ({ page }) => {
    await page.goto('/app')
    // real /api/ai/models: the seed state has no key
    await expect(page.getByText('Run a command')).toBeVisible()
    await expect(page.getByTestId('butler-fab')).toHaveCount(0)
  })

  test('fab opens sheet with presets on home', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')
    await expect(page.getByTestId('butler-fab')).toBeVisible()
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await expect(page.getByTestId('butler-preset-Brief me')).toBeVisible()
    await expect(page.getByTestId('butler-prompt')).toBeVisible()
    await expect(page).toHaveScreenshot('butler-sheet.png')
  })

  test('clicking outside closes the popup on desktop', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await page.mouse.click(20, 20)
    await expect(page.getByTestId('butler-sheet')).toHaveCount(0)
  })

  test('project hint chip shows and clears', async ({ page }) => {
    await mockProject(page, FAKE_ID)
    await mockButlerThreads(page)
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-hint')).toContainText(`looking at: ${FAKE_ID}`)
    await page.getByTestId('butler-hint-clear').click()
    await expect(page.getByTestId('butler-hint')).toHaveCount(0)
  })

  test('turn round-trip renders answer', async ({ page }) => {
    await mockProject(page, FAKE_ID)
    await mockButlerThreads(page)
    await mockButlerTurn(page, { answer: 'All three projects are healthy.' })
    await page.getByTestId('butler-fab').click()
    await page.getByTestId('butler-prompt').fill('Brief me')
    await page.getByTestId('butler-send').click()
    await expect(page.getByTestId('butler-answer')).toContainText('All three projects are healthy.')
    await expect(page).toHaveScreenshot('butler-answer.png')
  })
})

test.describe('butler phone', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true })

  test('sheet opens bottom-anchored on phone', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')
    await page.getByTestId('butler-fab').click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
    await expect(page.getByTestId('butler-prompt')).toBeVisible()
    await expect(page).toHaveScreenshot('butler-sheet-phone.png')
  })
})

export { TID }
