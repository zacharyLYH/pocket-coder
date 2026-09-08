import { mkdirSync, writeFileSync } from 'node:fs'
import type {
  FullConfig,
  FullResult,
  Reporter,
  Suite,
  TestCase,
  TestResult,
} from '@playwright/test/reporter'

// Live progress checklist for one parallel e2e group.
//
// e2e-parallel.sh runs one Playwright process per group with E2E_RUN_ID set;
// this reporter maintains test-results/<run>/progress.md so a stuck group
// can be inspected without reading the whole log:
//
//   - [x] test title (12s)      passed
//   - [~] test title (45s)      running now, with live elapsed
//   - [~] test title (200s) ⚠ LONG   running over LONG_THRESHOLD
//   - [!] test title (180s)     failed
//   - [ ] test title            not started
//   - [-] test title            skipped
//
// Rewritten on begin / test-begin / test-end / end, plus a 5s ticker so a
// test stuck with no events still shows a climbing timer — that is how
// overlong runs get flagged.
const RUN_ID = process.env.E2E_RUN_ID ?? 'default'
const DIR = `test-results/${RUN_ID}`
const FILE = `${DIR}/progress.md`

// A test running longer than this is flagged LONG: Playwright's per-test
// timeout is 180s, so anything past ~2min deserves investigation.
const LONG_THRESHOLD_S = 120
const TICK_MS = 5_000

type Status = 'pending' | 'running' | 'passed' | 'failed' | 'skipped'

function icon(s: Status): string {
  switch (s) {
    case 'passed': return 'x'
    case 'failed': return '!'
    case 'running': return '~'
    case 'skipped': return '-'
    case 'pending': return ' '
  }
}

export default class ProgressReporter implements Reporter {
  private order: string[] = []
  private status = new Map<string, Status>()
  private duration = new Map<string, number>()
  private startedAt = new Map<string, number>()
  private runStartedAt = Date.now()
  private ticker: ReturnType<typeof setInterval> | null = null

  onBegin(_config: FullConfig, suite: Suite): void {
    for (const t of suite.allTests()) {
      const key = t.titlePath().slice(1).join(' › ')
      if (!this.status.has(key)) {
        this.status.set(key, 'pending')
        this.order.push(key)
      }
    }
    this.render('running')
    // Ticker keeps running items' timers climbing even when no test
    // events fire (the stuck case). Unref'd so it never holds the
    // process open; cleared on end.
    this.ticker = setInterval(() => this.render('running'), TICK_MS)
    this.ticker.unref()
  }

  onTestBegin(test: TestCase): void {
    const key = test.titlePath().slice(1).join(' › ')
    this.status.set(key, 'running')
    this.startedAt.set(key, Date.now())
    this.render('running')
  }

  onTestEnd(test: TestCase, result: TestResult): void {
    const key = test.titlePath().slice(1).join(' › ')
    this.duration.set(key, result.duration)
    this.startedAt.delete(key)
    this.status.set(
      key,
      result.status === 'passed' ? 'passed'
      : result.status === 'skipped' ? 'skipped'
      : 'failed',
    )
    this.render('running')
  }

  onEnd(result: FullResult): void {
    if (this.ticker) clearInterval(this.ticker)
    this.render(result.status)
  }

  private render(overall: string): void {
    const done = [...this.status.values()].filter((s) => s !== 'pending' && s !== 'running').length
    const total = this.order.length
    const elapsed = Math.round((Date.now() - this.runStartedAt) / 1000)
    const lines = [
      `# e2e progress: ${RUN_ID}`,
      '',
      `status: ${overall} — ${done}/${total} done in ${elapsed}s`,
      '',
      ...this.order.map((key) => {
        const s = this.status.get(key) ?? 'pending'
        if (s === 'running') {
          const secs = Math.round((Date.now() - (this.startedAt.get(key) ?? Date.now())) / 1000)
          const flag = secs >= LONG_THRESHOLD_S ? ' ⚠ LONG' : ''
          return `- [${icon(s)}] ${key} (${secs}s)${flag}`
        }
        const ms = this.duration.get(key)
        const secs = ms !== undefined ? ` (${(ms / 1000).toFixed(0)}s)` : ''
        return `- [${icon(s)}] ${key}${secs}`
      }),
      '',
    ]
    mkdirSync(DIR, { recursive: true })
    writeFileSync(FILE, lines.join('\n'))
  }
}
