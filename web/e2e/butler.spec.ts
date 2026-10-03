import { expect, test } from './test'

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
    // Server owns the transcript: send posts, then the sheet adopts the
    // persisted thread via re-open — so the opened thread must carry the
    // turn (the POST body itself is never rendered).
    await mockButlerThreads(page, [{ prompt: 'Brief me', answer: 'All three projects are healthy.' }])
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

  test('fab is draggable and snaps to nearest corner on release', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')

    const fab = page.getByTestId('butler-fab')
    await expect(fab).toBeVisible()

    // Start at bottom-right (default position).
    const startBox = await fab.boundingBox()
    expect(startBox).not.toBeNull()
    const startX = startBox!.x + startBox!.width / 2
    const startY = startBox!.y + startBox!.height / 2

    // Drag toward bottom-left corner and verify the FAB moves.
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(40, 780, { steps: 20 })

    // FAB should have visually moved during the drag.
    const midBox = await fab.boundingBox()
    expect(midBox!.x).toBeLessThan(startBox!.x - 50)

    await page.mouse.up()

    // After release, the FAB snaps to the bottom-left corner.
    const endBox = await fab.boundingBox()
    expect(endBox!.x).toBeLessThan(50)
    expect(endBox!.y).toBeGreaterThan(750)
  })

  test('clicking the fab still opens the sheet after a non-drag click', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')

    const fab = page.getByTestId('butler-fab')
    await expect(fab).toBeVisible()
    await fab.click()
    await expect(page.getByTestId('butler-sheet')).toBeVisible()
  })

  test('fab drags from bottom-left to top-right corner', async ({ page }) => {
    await mockConfigured(page)
    await page.goto('/app')

    const fab = page.getByTestId('butler-fab')
    await expect(fab).toBeVisible()

    // Move the FAB to bottom-left first (via a drag).
    let box = await fab.boundingBox()
    expect(box).not.toBeNull()
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2)
    await page.mouse.down()
    await page.mouse.move(40, 780, { steps: 20 })
    await page.mouse.up()

    // Verify it snapped to bottom-left.
    box = await fab.boundingBox()
    expect(box!.x).toBeLessThan(50)
    expect(box!.y).toBeGreaterThan(750)

    // Now drag from bottom-left to top-right.
    const startBox = await fab.boundingBox()
    const startX = startBox!.x + startBox!.width / 2
    const startY = startBox!.y + startBox!.height / 2
    await page.mouse.move(startX, startY)
    await page.mouse.down()
    await page.mouse.move(350, 40, { steps: 20 })

    // FAB should have visually moved up and right during the drag.
    const midBox = await fab.boundingBox()
    expect(midBox!.y).toBeLessThan(startBox!.y - 50)
    expect(midBox!.x).toBeGreaterThan(startBox!.x + 50)

    await page.mouse.up()

    // After release, the FAB snaps to the top-right corner,
    // and a drag must not open the sheet.
    const endBox = await fab.boundingBox()
    expect(endBox!.x).toBeGreaterThan(290)
    expect(endBox!.y).toBeLessThan(50)
    await expect(page.getByTestId('butler-sheet')).toHaveCount(0)

    // No persistence: a reload starts back at the bottom-right default.
    await page.reload()
    await expect(page.getByTestId('butler-fab')).toBeVisible()
    const freshBox = await page.getByTestId('butler-fab').boundingBox()
    expect(freshBox!.x).toBeGreaterThan(290)
    expect(freshBox!.y).toBeGreaterThan(750)
  })
})

export { TID }
