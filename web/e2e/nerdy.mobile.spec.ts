import { expect, test } from '@playwright/test'

import { mockNerdy, terminalUrl, PROJECT } from './nerdy.fixtures'

// Mobile Nerdy Stuff (390x844): route-mocked, no engine. Tail → chip filter
// → expand → copy trace → Load older → Follow pause/resume → screenshot.
test.describe('nerdy stuff (mobile)', () => {
  test.use({ viewport: { width: 390, height: 844 }, hasTouch: true, isMobile: true })

  test.beforeEach(async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await mockNerdy(page)
    await page.goto(terminalUrl(PROJECT, 'main'))
    await page.getByTestId('tab-nerdy').click()
    await expect(page.getByTestId('nerdy-list')).toBeVisible({ timeout: 10_000 })
  })

  test('tail → chip filter → expand → copy trace → load older → follow', async ({ page }) => {
    // tail shows the mocked lines
    await expect(page.getByTestId('nerdy-list')).toContainText('health check passed')

    // chip filter narrows to errors (chips hide behind Filters on mobile)
    await page.getByTestId('nerdy-filters-toggle').click()
    await page.getByTestId('nerdy-level-error').click()
    // picking a chip collapses the section again (dropdown behavior)
    await expect(page.getByTestId('nerdy-level-error')).toBeHidden()
    await expect(page.getByTestId('nerdy-filters-toggle')).toContainText('Filters (1)')
    await expect(page.getByTestId('nerdy-list')).toContainText('clone failed')
    await expect(page.getByTestId('nerdy-list')).not.toContainText('health check passed')

    // expand a row → JSON + copy trace
    await page.getByTestId('nerdy-row-toggle-8').click()
    await expect(page.getByTestId('nerdy-expand-8')).toContainText('project.clone')
    await page.getByTestId('nerdy-copy-trace').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('3333333333333333')

    // clear filter → Load older pages the before cursor
    await page.getByTestId('nerdy-filters-toggle').click()
    await page.getByTestId('nerdy-level-all').click()
    await expect(page.getByTestId('nerdy-load-older')).toBeVisible()
    await page.getByTestId('nerdy-load-older').click()
    await expect(page.getByTestId('nerdy-row-1')).toBeVisible()

    // Follow pauses and resumes
    await page.getByTestId('nerdy-follow').click()
    await expect(page.getByTestId('nerdy-resume')).toBeVisible()
    await page.getByTestId('nerdy-resume').click()
    await expect(page.getByTestId('nerdy-follow')).toContainText('Following')

    await expect(page.getByTestId('nerdy-tab')).toHaveScreenshot('nerdy-mobile-runtime.png')
  })

  test('errors inbox jumps to the sampled trace', async ({ page }) => {
    await page.getByTestId('nerdy-panel-select').click()
    await page.getByTestId('nerdy-panel-opt-errors').click()
    await expect(page.getByTestId('nerdy-error-project.clone')).toBeVisible()
    await page.getByTestId('nerdy-error-project.clone').click()
    await expect(page.getByTestId('nerdy-list')).toBeVisible({ timeout: 10_000 })
    await expect(page).toHaveScreenshot('nerdy-mobile-errors.png', { fullPage: true })
  })
})
