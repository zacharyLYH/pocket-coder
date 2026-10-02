import { createServer, type IncomingMessage, type Server, type ServerResponse } from 'node:http'

import { expect, test, type Page } from './test'

import { mockSessions } from './mocks'
import { terminalUrl } from './helpers'

// Butler LIVE-backend e2e: everything is real — Go server, transcript
// store, plain-JSON turns — except the LLM, which is a scripted
// OpenAI-compatible stub on the host reached via host.docker.internal.
//
// The stub plays a big turn: 4 slow tool rounds then a multi-section
// briefing, so the pending skeleton is observable mid-run before the final
// answer lands.
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

type ToolCall = { id: string; type: string; function: { name: string; arguments: string } }

function toolCall(id: string, name: string, args: string): ToolCall {
  return { id, type: 'function', function: { name, arguments: args } }
}

function toolResult(id: string, content: string) {
  return { role: 'tool', tool_call_id: id, content }
}

// The read tools the rounds name, plus the one write the approval test
// proposes. Declared on every request: the loop needs a non-empty tools
// array to accept tool_calls at all.
const TOOLS = ['list_projects', 'events_tail', 'project_detail', 'health', 'preview_close'].map((name) => ({
  type: 'function',
  function: { name, description: `e2e stub tool ${name}`, parameters: { type: 'object', properties: {} } },
}))

type Round = { calls: ToolCall[]; results: ReturnType<typeof toolResult>[] }

// startFakeLLM serves /chat/completions with a scripted plan: call 0 is
// the model-create probe, call 1 is the structured scope gate (allow
// verdict), the next calls play the given tool rounds, then the final
// answer. Each round responds with tool_calls; the server executes them
// and posts back tool results, which the stub ignores — the plan is fixed
// regardless of what came back.
async function startFakeLLM(rounds: Round[], final: string): Promise<{ server: Server; url: string; calls: () => number }> {
  let calls = 0
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
        const isScope = n === 1
        const round = !isProbe && !isScope && n <= rounds.length + 1 ? rounds[n - 2] : null
        await new Promise((r) => setTimeout(r, isProbe ? 0 : 700))
        const scopeContent = '{"can_help":true}'
        const message: { role: string; content: string; tool_calls?: ToolCall[] } =
          round ? { role: 'assistant', content: '', tool_calls: round.calls } : { role: 'assistant', content: isProbe ? '{"ok":true}' : isScope ? scopeContent : final }
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

  // Seed one model pointed at the stub and return its id. Create probes
  // it live, so this is LLM call 1 and proves the container reaches the
  // host. Callers delete the model in cleanup.
  async function seedModel(page: Page, url: string): Promise<string> {
    const created = await page.request.post('/api/ai/models', {
      data: { label: 'e2e-mini', baseURL: url, apiKey: 'e2e-fake-key', model: 'e2e-mini' },
    })
    expect(created.status()).toBe(201)
    return ((await created.json()) as { id: string }).id
  }

  // Keep the shared backend pristine for the other specs in this group
  // (e.g. codemap's no-key test needs zero models).
  async function cleanup(page: Page, llm: { server: Server }, modelId: string) {
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

  test('runs a multi-round turn against the real backend', async ({ page }) => {
    const llm = await startFakeLLM(
      [
        { calls: [toolCall('call-1', 'list_projects', '{}')], results: [toolResult('call-1', '[]')] },
        { calls: [toolCall('call-2', 'events_tail', '{"limit":20}')], results: [toolResult('call-2', '[]')] },
        { calls: [toolCall('call-3', 'project_detail', '{"id":"api"}')], results: [toolResult('call-3', '{}')] },
        { calls: [toolCall('call-4', 'health', '{}')], results: [toolResult('call-4', '{}')] },
      ],
      ANSWER,
    )
    let modelId = ''
    try {
      modelId = await seedModel(page, llm.url)

      await mockSessions(page)
      await page.goto(terminalUrl(FAKE_ID, 'main'))
      await page.getByTestId('butler-fab').click()
      await page.getByTestId('butler-prompt').fill(PROMPT)
      await page.getByTestId('butler-send').click()

      // In-flight window: the stub answers slowly, so the pending
      // indicator is still up ~2s in, long before the final answer lands.
      await expect(page.getByTestId('butler-pending')).toBeVisible()
      await page.waitForTimeout(2000)
      await expect(page.getByTestId('butler-pending')).toBeVisible()
      await expect(page).toHaveScreenshot('butler-live-streaming.png')

      await expect(page.getByTestId('butler-answer')).toContainText('1 needs attention', { timeout: 15000 })
      await expect(page.getByTestId('butler-answer')).toContainText('Suggested next step')
      await expect(page).toHaveScreenshot('butler-live-answer.png')

      // The turn really persisted server-side: probe + scope + 4 rounds + final.
      const listed = await page.request.get('/api/butler/threads')
      const threads = (((await listed.json()) as { threads: unknown[] }).threads ?? [])
      expect(threads).toHaveLength(1)
      expect(llm.calls()).toBeGreaterThanOrEqual(7)
    } finally {
      await cleanup(page, llm, modelId)
    }
  })

  test('proposes a write and discards it against the real backend', async ({ page }) => {
    // preview_close has a pure blast radius (no container needed), so the
    // propose succeeds for a fake id — and discard mutates nothing, making
    // the whole approval round-trip safe to run live.
    const llm = await startFakeLLM(
      [
        { calls: [toolCall('call-1', 'preview_close', `{"project":"${FAKE_ID}"}`)], results: [toolResult('call-1', '{}')] },
      ],
      'Tap Confirm to close the preview.',
    )
    let modelId = ''
    try {
      modelId = await seedModel(page, llm.url)

      await mockSessions(page)
      await page.goto(terminalUrl(FAKE_ID, 'main'))
      await page.getByTestId('butler-fab').click()
      await page.getByTestId('butler-prompt').fill('close the preview')
      await page.getByTestId('butler-send').click()

      await expect(page.getByTestId('butler-confirm')).toBeVisible({ timeout: 15000 })
      await expect(page.getByTestId('butler-confirm-summary')).toContainText(`Close preview for ${FAKE_ID}?`)
      await expect(page.getByTestId('butler-confirm-blast')).toContainText('app server keeps running')
      await expect(page.getByTestId('butler-approval-block')).toBeVisible()
      await expect(page.getByTestId('butler-prompt')).toBeDisabled()

      await page.getByTestId('butler-confirm-no').click()
      await expect(page.getByTestId('butler-confirm')).toHaveCount(0)
      await expect(page.getByTestId('butler-prompt')).toBeEnabled({ timeout: 10000 })

      // Server-side: the card resolved to discarded, the turn stayed put,
      // and the thread is back to ready with nothing applied.
      const listed = await page.request.get('/api/butler/threads')
      const threads = (((await listed.json()) as { threads: { id: string }[] }).threads ?? [])
      expect(threads).toHaveLength(1)
      const got = await page.request.get(`/api/butler/threads/${threads[0].id}`)
      const thread = ((await got.json()) as { thread: { turns: unknown[]; approvals: unknown[]; status: string } }).thread
      expect(thread.turns).toHaveLength(1)
      expect(thread.approvals).toHaveLength(0)
      expect(thread.status).toBe('ready')
    } finally {
      await cleanup(page, llm, modelId)
    }
  })
})
