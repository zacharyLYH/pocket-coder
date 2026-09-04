import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createReactProject, openPreviewFromTerminal } from './preview.helpers'

test.describe('preview auth and isolation', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('preview status requires authentication', async ({ browser }) => {
    const freshCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const freshPage = await freshCtx.newPage()
    const res = await freshPage.request.get('/api/projects/nonexistent/preview')
    expect([401, 403, 404]).toContain(res.status())
    await freshPage.close()
    await freshCtx.close()
  })

  test('tools require authentication', async ({ browser }) => {
    const freshCtx = await browser.newContext({ storageState: { cookies: [], origins: [] } })
    const freshPage = await freshCtx.newPage()
    const getRes = await freshPage.request.get('/api/projects/nonexistent/preview/tools/screenshot')
    expect([401, 403, 404, 502]).toContain(getRes.status())
    const postRes = await freshPage.request.post('/api/projects/nonexistent/preview/tools/navigate', { data: {} })
    expect([401, 403, 404, 502]).toContain(postRes.status())
    await freshPage.close()
    await freshCtx.close()
  })

  test('authenticated user can capture screenshot', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const _previewPage = await openPreviewFromTerminal(page, projectID)
      await _previewPage.close()

      const res = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(res.ok()).toBeTruthy()
      expect(res.headers()['content-type']).toContain('image/png')
    } finally {
      await deleteAllProjects(request)
    }
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
      const idB = await createReactProject(request)

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

  test('raw CDP endpoint is not exposed through SPS response', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const _previewPage = await openPreviewFromTerminal(page, projectID)
      await _previewPage.close()

      const res = await request.get(`/api/projects/${projectID}/preview`)
      expect(res.ok()).toBeTruthy()
      const body = await res.json()
      expect(JSON.stringify(body)).not.toMatch(/922[0-9]/)
      expect(JSON.stringify(body)).not.toContain('ws://')
      expect(JSON.stringify(body)).not.toContain('127.0.0.1')
    } finally {
      await deleteAllProjects(request)
    }
  })
})
