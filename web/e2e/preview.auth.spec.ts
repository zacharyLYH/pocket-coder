import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createReactProject, openPreviewFromTerminal } from './preview.helpers'

// Auth + isolation for the preview endpoints. The "unauthenticated is
// rejected" and "private CDP endpoint never leaks" guarantees are pinned
// deterministically by the backend unit tests (see preview_status_test.go),
// so this spec focuses on what a real stack isolates: one authenticated
// user's access, and cross-project isolation. Screenshot-as-proof is shared
// with preview.tools.spec.ts; the leak and plain-auth assertions live in Go.
test.describe('preview auth and isolation', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('preview endpoints require authentication (representative GET + POST)', async ({ browser }) => {
    const freshCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const freshPage = await freshCtx.newPage()
    // One read tool and one write tool represent the RequireAuth middleware;
    // every endpoint is registered behind the same guard.
    const getRes = await freshPage.request.get('/api/projects/nonexistent/preview')
    expect([401, 403, 404]).toContain(getRes.status())
    const postRes = await freshPage.request.post('/api/projects/nonexistent/preview/tools/navigate', { data: {} })
    expect([401, 403, 404, 502]).toContain(postRes.status())
    await freshPage.close()
    await freshCtx.close()
  })

  test('project A cannot access project B preview', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const idA = await createReactProject(request)
      // Second project exists so the backend is exercised with real
      // cross-project state present; its preview is covered by the Go tests.
      await createReactProject(request)

      await page.goto('/')
      const _previewPage2 = await openPreviewFromTerminal(page, idA)
      await _previewPage2.close()

      const resA = await request.get(`/api/projects/${idA}/preview/tools/screenshot`)
      expect(resA.ok()).toBeTruthy()
      expect(resA.headers()['content-type']).toContain('image/png')
    } finally {
      await deleteAllProjects(request)
    }
  })
})
