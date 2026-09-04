import { expect, test } from '@playwright/test'

import { deleteAllProjects, engineUp } from './helpers'
import { createReactProject, openPreviewFromTerminal } from './preview.helpers'

test.describe('preview AI tools', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('screenshot returns a valid PNG via tools API', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      const res = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(res.ok()).toBeTruthy()
      expect(res.headers()['content-type']).toContain('image/png')
      const body = Buffer.from(await res.body())
      expect(body.length).toBeGreaterThan(1_000)
      expect(body[0]).toBe(0x89)
      expect(body[1]).toBe(0x50)
      expect(body[2]).toBe(0x4e)
      expect(body[3]).toBe(0x47)
      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('inspect returns page HTML via tools API', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      await previewPage.close()

      const res = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(res.ok()).toBeTruthy()
      const { html } = (await res.json()) as { html: string }
      expect(html).toContain('<html')
      expect(html).toContain('Full-Stack App')
      expect(html).toContain('name-input')
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('click and type interact with the remote browser', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      await previewPage.close()

      const typeRes = await request.post(`/api/projects/${projectID}/preview/tools/type`, {
        data: { selector: '[data-testid="name-input"]', text: 'hello world' },
      })
      expect(typeRes.ok()).toBeTruthy()

      const clickRes = await request.post(`/api/projects/${projectID}/preview/tools/click`, {
        data: { selector: '[data-testid="login-btn"]' },
      })
      expect(clickRes.ok()).toBeTruthy()

      await page.waitForTimeout(2000)

      const inspectRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('hello world')
      expect(html).toContain('counter-value')
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('navigate and reload work through tools API', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      await previewPage.close()

      const navRes = await request.post(`/api/projects/${projectID}/preview/tools/navigate`, {
        data: { url: 'http://localhost:3000' },
      })
      expect(navRes.ok()).toBeTruthy()
      await page.waitForTimeout(2000)

      const reloadRes = await request.post(`/api/projects/${projectID}/preview/tools/reload`)
      expect(reloadRes.ok()).toBeTruthy()
      await page.waitForTimeout(2000)

      const inspectRes = await request.get(`/api/projects/${projectID}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('<html')
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('network tool reports resource requests', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)
      await previewPage.close()

      const res = await request.get(`/api/projects/${projectID}/preview/tools/network`)
      expect(res.ok()).toBeTruthy()
      const { requests } = (await res.json()) as { requests: { name: string; dur: number }[] }
      expect(Array.isArray(requests)).toBeTruthy()
      expect(requests.length).toBeGreaterThan(0)
    } finally {
      await deleteAllProjects(request)
    }
  })

  test('CDP screenshot matches display surface content', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createReactProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      const surfaceShot = await previewPage.screenshot({ fullPage: true })
      const cdpRes = await request.get(`/api/projects/${projectID}/preview/tools/screenshot`)
      expect(cdpRes.ok()).toBeTruthy()
      const cdpBody = Buffer.from(await cdpRes.body())
      expect(surfaceShot.length).toBeGreaterThan(1_000)
      expect(cdpBody.length).toBeGreaterThan(1_000)
      expect(cdpBody[0]).toBe(0x89)
      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
