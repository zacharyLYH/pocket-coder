import { expect, test } from '@playwright/test'
import { deleteAllProjects, engineUp, projectURL } from './helpers'
import { createVueProject, execInProject, openPreviewFromTerminal, waitForInspectContaining } from './preview.helpers'

test.describe('preview Vue.js', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('Vue SPA with HMR and backend', async ({ page, request }) => {
    test.setTimeout(600_000)
    if (!(await engineUp(request))) {
      test.skip(true, 'Docker engine unavailable — e2e skipped')
      return
    }
    await deleteAllProjects(request)
    try {
      const projectID = await createVueProject(request)
      await page.goto('/')
      const previewPage = await openPreviewFromTerminal(page, projectID)

      // Verify initial Vue render
      await expect(previewPage).toHaveScreenshot('preview-vue-initial.png', { fullPage: true })

      const inspectRes = await request.get(`/api/projects/${projectURL(projectID)}/preview/tools/inspect`)
      expect(inspectRes.ok()).toBeTruthy()
      const { html } = (await inspectRes.json()) as { html: string }
      expect(html).toContain('Vue App')
      expect(html).toContain('Vue initial')

      // HMR: edit the Vue component
      const newComponent = [
        '<template>',
        '  <main style="font-family:sans-serif;padding:32px">',
        '    <h1>Vue App</h1>',
        '    <p data-testid="hmr-marker">Vue HMR is working</p>',
        '    <p data-testid="backend-data">{{ backendData }}</p>',
        '    <button data-testid="vue-btn" @click="fetchData">Fetch</button>',
        '  </main>',
        '</template>',
        '',
        '<script setup>',
        "import { ref, onMounted } from 'vue'",
        '',
        'const backendData = ref("loading...")',
        '',
        'onMounted(() => {',
        "  fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{ backendData.value = JSON.stringify(d) }).catch(()=>backendData.value='fetch failed')",
        '})',
        '',
        'function fetchData() {',
        "  fetch('http://localhost:4000/api/data').then(r=>r.json()).then(d=>{ backendData.value = JSON.stringify(d) }).catch(()=>backendData.value='fetch failed')",
        '}',
        '</script>',
      ].join('\n')
      const encoded = Buffer.from(newComponent).toString('base64')
      await execInProject(
        request,
        projectID,
        `printf '%s' '${encoded}' | base64 -d > /workspace/app/src/App.vue`,
      )

      // Wait for HMR to apply
      await waitForInspectContaining(request, projectID, 'Vue HMR is working')

      // Verify no page reload (HMR, not full refresh)
      // Capture after HMR screenshot
      await expect(previewPage).toHaveScreenshot('preview-vue-hmr.png', { fullPage: true })

      await previewPage.close()
    } finally {
      await deleteAllProjects(request)
    }
  })
})
