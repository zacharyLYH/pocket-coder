import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import { expect, test, type Page } from '@playwright/test'

import { terminalUrl } from './helpers'

// Butler LIVE-backend e2e: everything is real — Go server, transcript
// store, SSE streaming — except the LLM, which is a scripted
// OpenAI-compatible stub on the host. The container reaches it via
// host.docker.internal (extra_hosts in docker-compose.e2e.yml).
//
// The stub plays a BIG turn: 4 slow tool rounds then a multi-section
// briefing, so ~6 SSE status lines stream over ~3.5s and the pending
// skeleton is observable mid-run before the final answer lands.
//
// The rounds are a real OpenAI tool round-trip: the request declares the
// ckpt-5 read tools and each tool_calls message is followed by tool-role
// result messages, so the server loop executes it as designed. (The
// current checkpoint wires zero server tools, so each call comes back
// "unknown tool" — still a completed round for the loop.)
const FAKE_ID = 'e2e/butler-live'
const PROMPT = 'Brief me on all my projects: which ones need attention, and what is the session and preview state?'

const ANSWER =
  'Fleet briefing — 3 projects, 1 needs attention.\n' +
  '\n' +
  'Needs attention:\n' +
  '- api is behind origin/main by 4 commits with 7 changed files; last push 2 days ago.\n' +
  '\n' +
  'Healthy:\n' +
  '- web is clean on main with 1 live session (main) and preview :5173 listening.\n' +
  '- worker has 0 sessions and no open previews; disk free 41 GB, uptime 6 days.\n' +
  '\n' +
  'Suggested next step: open api and run git pull, then restart its preview.'

// mockSessions keeps the terminal pane quiet on a project that does not
// exist (same as butler.spec.ts): UI scaffolding only, not the feature.
async function mockSessions(page: Page) {
  await page.route('**/api/projects/*/sessions', async (route) => {
    if (route.request().method() === 'POST') {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ name: 'main' }) })
    } else {
      await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ sessions: [{ name: 'main' }] }) })
    }
  })
}

type ToolCall = { id: string; type: string; function: { name: string; arguments: string } }

function toolCall(id: string, name: string, args: string): ToolCall {
  return { id, type: 'function', function: { name, arguments: args } }
}

function toolResult(id: string, content: string) {
  return { role: 'tool', tool_call_id: id, content }
}

// The ckpt-5 read tools the rounds name. Declared on every request: the
// loop needs a non-empty tools array to accept tool_calls at all.
const TOOLS = ['list_projects', 'events_tail', 'project_detail', 'health'].map((name) => ({
  type: 'function',
  function: { name, description: `e2e stub tool ${name}`, parameters: { type: 'object', properties: {} } },
}))

// startFakeLLM serves /chat/completions with a scripted plan: call 0 is
// the model-create probe, calls 1-4 are slow tool rounds (one SSE status
// line each on the backend), call 5 is the final briefing. Each round
// responds with tool_calls; the server executes them (unknown-tool
// replies today) and posts back tool results, which the stub ignores —
// the plan is fixed regardless of what came back.
async function startFakeLLM(): Promise<{ server: Server; url: string; calls: () => number }> {
  let calls = 0
  const rounds: { calls: ToolCall[]; results: ReturnType<typeof toolResult>[] }[] = [
    { calls: [toolCall('call-1', 'list_projects', '{}')], results: [toolResult('call-1', '[]')] },
    { calls: [toolCall('call-2', 'events_tail', '{"limit":20}')], results: [toolResult('call-2', '[]')] },
    { calls: [toolCall('call-3', 'project_detail', '{"id":"api"}')], results: [toolResult('call-3', '{}')] },
    { calls: [toolCall('call-4', 'health', '{}')], results: [toolResult('call-4', '{}')] },
  ]
  const server = createServer((req: IncomingMessage, res: ServerResponse) => {
    if (req.method !== 'POST' || !req.url?.endsWith('/chat/completions')) {
      res.writeHead(404).end()
      return
    }
    req.resume()
    req.on('end', () => {
      void (async () => {
        const n = calls++
        const isProbe = n === 0
        const round = !isProbe && n <= rounds.length ? rounds[n - 1] : null
        await new Promise((r) => setTimeout(r, isProbe ? 0 : 700))
        const message: { role: string; content: string; tool_calls?: ToolCall[] } =
          round ? { role: 'assistant', content: '', tool_calls: round.calls } : { role: 'assistant', content: isProbe ? '{"ok":true}' : ANSWER }
        const choice: Record<string, unknown> = { index: 0, finish_reason: round ? 'tool_calls' : 'stop', message }
        if (round) choice.tool_results = round.results // consumed by the harness below
        res.writeHead(200, { 'Content-Type': 'application/json' })
        res.end(JSON.stringify({
          id: 'chatcmpl-e2e', object: 'chat.completion', created: 1, model: 'e2e-mini',
          tools: TOOLS,
          choices: [choice],
        }))
      })()
    })
  })
  await new Promise<void>((resolve) => server.listen(0, '0.0.0.0', resolve))
  const port = (server.address() as { port: number }).port
  return { server, url: `http://host.docker.internal:${port}`, calls: () => calls }
}

test.describe('butler live backend', () => {
  test.use({ viewport: { width: 1280, height: 720 }, timezoneId: 'UTC' })

  test('streams a multi-round turn against the real backend', async ({ page }) => {
    const llm = await startFakeLLM()
    let modelId = ''
    try {
      // Seed one model pointed at the stub. Create probes it live, so
      // this is LLM call 1 and proves the container reaches the host.
      const created = await page.request.post('/api/ai/models', {
        data: { label: 'e2e-mini', baseURL: llm.url, apiKey: 'e2e-fake-key', model: 'e2e-mini' },
      })
      expect(created.status()).toBe(201)
      modelId = ((await created.json()) as { id: string }).id

      await mockSessions(page)
      await page.goto(terminalUrl(FAKE_ID, 'main'))
      await page.getByTestId('butler-fab').click()
      await page.getByTestId('butler-prompt').fill(PROMPT)
      await page.getByTestId('butler-send').click()

      // Streaming window: the stub answers slowly, so the pending
      // skeleton is still up ~2s in, long before the final SSE line.
      await expect(page.getByTestId('butler-pending')).toBeVisible()
      await page.waitForTimeout(2000)
      await expect(page.getByTestId('butler-pending')).toBeVisible()
      await expect(page).toHaveScreenshot('butler-live-streaming.png')

      await expect(page.getByTestId('butler-answer')).toContainText('1 needs attention', { timeout: 15000 })
      await expect(page.getByTestId('butler-answer')).toContainText('Suggested next step')
      await expect(page).toHaveScreenshot('butler-live-answer.png')

      // The turn really persisted server-side: probe + 4 rounds + final.
      const listed = await page.request.get('/api/butler/threads')
      const threads = (((await listed.json()) as { threads: unknown[] }).threads ?? [])
      expect(threads).toHaveLength(1)
      expect(llm.calls()).toBeGreaterThanOrEqual(6)
    } finally {
      // Keep the shared backend pristine for the other specs in this
      // group (e.g. codemap's no-key test needs zero models).
      try {
        const listed = await page.request.get('/api/butler/threads')
        const threads = (((await listed.json()) as { threads: { id: string }[] }).threads ?? [])
        for (const t of threads) await page.request.delete(`/api/butler/threads/${t.id}`)
      } catch { /* cleanup must not fail the test */ }
      try {
        if (modelId) await page.request.delete(`/api/ai/models/${modelId}`)
      } catch { /* cleanup must not fail the test */ }
      await new Promise((resolve) => llm.server.close(resolve))
    }
  })
})
