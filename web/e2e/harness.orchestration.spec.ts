import { expect, test, type APIRequestContext } from '@playwright/test'
import { deleteAllProjects, engineUp } from './helpers'

// Multi-project orchestration end states, against the real backend and real
// containers. The UI-level picking flows live in harness.inject.spec.ts;
// this spec drives combinations through the same HTTP surface the UI uses
// and asserts EXACT final state after each step:
//
//   1. three projects; nothing pre-installed anywhere
//   2. adding a harness to the REGISTRY touches NO project — it shows up
//      everywhere as "not installed"
//   3. installing into a subset flips exactly those projects
//   4. a batch command runs in every selected running container; its output
//      comes back per project
//   5. a stopped container is SKIPPED by batch runs, not errored
//   6. deleting one project leaves the others byte-for-byte intact
//
// The vehicle is a purpose-made "combo agent": instant local install, so
// the whole flow runs in seconds while still going through the real
// install+validate+exec pipeline.

type HarnessRow = { id: string; installed: boolean }

async function createProject(request: APIRequestContext): Promise<string> {
  const res = await request.post('/api/projects', { data: {} })
  expect(res.ok()).toBeTruthy()
  return ((await res.json()) as { id: string }).id
}

async function harnessStates(request: APIRequestContext, id: string): Promise<Record<string, boolean>> {
  const res = await request.get(`/api/projects/${id}/harnesses`)
  expect(res.ok()).toBeTruthy()
  const body = (await res.json()) as { harnesses: HarnessRow[] }
  return Object.fromEntries(body.harnesses.map((h) => [h.id, h.installed]))
}

async function waitForRunning(request: APIRequestContext, id: string): Promise<void> {
  for (let i = 0; i < 60; i++) {
    const res = await request.get(`/api/projects/${id}`)
    if (res.ok() && ((await res.json()) as { status: string }).status === 'running') return
    await new Promise((r) => setTimeout(r, 500))
  }
  throw new Error(`project ${id} never reached running`)
}

test.describe('multi-project orchestration end states', () => {
  test.use({ viewport: { width: 1280, height: 720 } })

  test('registry vs installs vs batch commands across projects', async ({ request }) => {
    test.setTimeout(180_000) // three sandboxes, an install, several execs
    test.skip(!(await engineUp(request)), 'Docker engine unavailable')
    await deleteAllProjects(request)
    try {
      // --- 1. three fresh projects; builtins exist but nothing is installed ---
      const [a, b, c] = [await createProject(request), await createProject(request), await createProject(request)]
      for (const id of [a, b, c]) await waitForRunning(request, id)
      for (const id of [a, b, c]) expect((await harnessStates(request, id))['opencode']).toBe(false)

      // --- 2. adding to the registry does NOT install anywhere ---
      const installScript =
        `printf '#!/bin/sh\\nif [ $# -gt 0 ]; then echo "combo-agent 1.0"; exit 0; fi\\necho combo-agent running\\n' > /usr/local/bin/combo-agent && chmod +x /usr/local/bin/combo-agent`
      let res = await request.post('/api/harnesses', {
        data: { name: 'Combo Agent', command: 'combo-agent', install: installScript },
      })
      expect(res.status()).toBe(201)

      const registry = ((await (await request.get('/api/harnesses')).json()) as { harnesses: { id: string }[] }).harnesses
      expect(registry.map((h) => h.id)).toContain('combo-agent')
      // the registry entry exists, but every project still sees it absent
      for (const id of [a, b, c]) expect((await harnessStates(request, id))['combo-agent']).toBe(false)

      // --- 3. install into exactly [a, b]; c stays untouched ---
      res = await request.post('/api/harnesses/combo-agent/install', { data: { projectIds: [a, b] } })
      expect(res.ok()).toBeTruthy()
      const installResults = ((await res.json()) as { results: { status: string }[] }).results
      expect(installResults).toHaveLength(2)
      expect(installResults.every((r) => r.status === 'ok')).toBe(true)
      expect((await harnessStates(request, a))['combo-agent']).toBe(true)
      expect((await harnessStates(request, b))['combo-agent']).toBe(true)
      expect((await harnessStates(request, c))['combo-agent']).toBe(false)

      // re-installing into an already-installed project is fine (idempotent
      // download); installing into an unknown project errors without
      // touching the known ones
      res = await request.post('/api/harnesses/combo-agent/install', { data: { projectIds: ['ghost1234'] } })
      expect(res.ok()).toBeTruthy()
      expect(((await res.json()) as { results: { status: string }[] }).results).toEqual([
        { project: 'ghost1234', status: 'error', detail: 'no such project' },
      ])
      expect((await harnessStates(request, a))['combo-agent']).toBe(true)

      // --- 4. a batch command runs in EVERY selected container ---
      res = await request.post('/api/projects/exec', {
        data: { projectIds: [a, b, c], command: 'printf combo-marker > /workspace/marker.txt' },
      })
      expect(res.ok()).toBeTruthy()
      const writeResults = ((await res.json()) as { results: { status: string }[] }).results
      expect(writeResults).toHaveLength(3)
      expect(writeResults.every((r) => r.status === 'ok')).toBe(true)

      // read them back per container — the output IS the proof
      res = await request.post('/api/projects/exec', {
        data: { projectIds: [a, b, c], command: 'cat /workspace/marker.txt' },
      })
      const readResults = ((await res.json()) as { results: { project: string; status: string; detail?: string }[] }).results
      expect(readResults).toHaveLength(3)
      for (const r of readResults) {
        expect(r.status).toBe('ok')
        expect(r.detail).toBe('combo-marker')
      }

      // --- 5. a STOPPED container is skipped, not failed ---
      expect((await request.post(`/api/projects/${c}/stop`, { data: '' })).ok()).toBeTruthy()
      res = await request.post('/api/projects/exec', {
        data: { projectIds: [a, c], command: 'echo hi' },
      })
      const mixed = ((await res.json()) as { results: { status: string; detail?: string }[] }).results
      expect(mixed).toHaveLength(2)
      expect(new Set(mixed.map((r) => r.status))).toEqual(new Set(['ok', 'skipped']))

      // --- 6. deleting one project leaves the others exactly intact ---
      res = await request.delete(`/api/projects/${b}?scope=all`)
      expect(res.ok()).toBeTruthy()

      const list = ((await request.get('/api/projects').then((r) => r.json())) as { projects: { id: string }[] }).projects
      expect(list.map((p) => p.id).sort()).toEqual([a, c].sort())

      // a's container still runs, still installed, marker file intact
      await waitForRunning(request, a)
      expect((await harnessStates(request, a))['combo-agent']).toBe(true)
      const marker = await request.post('/api/projects/exec', {
        data: { projectIds: [a], command: 'cat /workspace/marker.txt' },
      })
      expect(((await marker.json()) as { results: { detail?: string }[] }).results[0].detail).toBe('combo-marker')

      // c restarts cleanly too — volumes survived the stop, still no combo-agent
      expect((await request.post(`/api/projects/${c}/start`, { data: '' })).ok()).toBeTruthy()
      await waitForRunning(request, c)
      expect((await harnessStates(request, c))['combo-agent']).toBe(false)

      // registry untouched by everything above
      const finalRegistry = ((await (await request.get('/api/harnesses')).json()) as { harnesses: { id: string }[] }).harnesses
      expect(finalRegistry.map((h) => h.id)).toContain('combo-agent')
    } finally {
      await deleteAllProjects(request)
    }
  })
})
