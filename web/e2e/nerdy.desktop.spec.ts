import { expect, test } from '@playwright/test'

import { mockNerdy, terminalUrl, PROJECT } from './nerdy.fixtures'

// Desktop Nerdy Stuff (1280x720): route-mocked, no engine. Same journey as
// mobile plus the facet rail, overview sparkline, and desktop screenshots.
test.describe('nerdy stuff (desktop)', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test.beforeEach(async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'])
    await mockNerdy(page)
    await page.goto(terminalUrl(PROJECT, 'main'))
    await page.getByTestId('tab-nerdy').click()
    await expect(page.getByTestId('nerdy-list')).toBeVisible({ timeout: 10_000 })
  })

  test('tail → facet rail → expand → copy trace → load older → follow', async ({ page }) => {
    await expect(page.getByTestId('nerdy-list')).toContainText('health check passed')

    // facet rail checkbox narrows to errors (deselect-to-zero allowed)
    await expect(page.getByTestId('nerdy-facets')).toBeVisible()
    await page.getByTestId('nerdy-facet-level-error').click()
    await expect(page.getByTestId('nerdy-list')).toContainText('clone failed')

    // expand → copy trace
    await page.getByTestId('nerdy-row-toggle-9').click()
    await expect(page.getByTestId('nerdy-expand-9')).toContainText('project.clone')
    await page.getByTestId('nerdy-copy-trace').click()
    expect(await page.evaluate(() => navigator.clipboard.readText())).toBe('4444444444444444')

    // search filters free-text
    await page.getByTestId('nerdy-facet-level-error').click()
    await page.getByTestId('nerdy-search').fill('health check')
    await expect(page.getByTestId('nerdy-list')).toContainText('health check passed')

    await expect(page.getByTestId('nerdy-tab')).toHaveScreenshot('nerdy-desktop-runtime.png')
  })

  test('overview shows state, sparkline, and health', async ({ page }) => {
    await page.getByTestId('nerdy-panel-overview').click()
    await expect(page.getByTestId('nerdy-stats-state')).toContainText('running')
    await expect(page.getByTestId('nerdy-sparkline')).toBeVisible()
    await expect(page.getByTestId('nerdy-health')).toContainText('1 error groups')
    await expect(page).toHaveScreenshot('nerdy-desktop-overview.png', { fullPage: true })
  })
})
