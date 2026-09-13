import { execFile, execFileSync, spawn } from 'node:child_process'
import { mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { GIT_PORT, RUN_DIR, RUN_SLUG } from './env'

// sweepOwnOrphans removes this run's leftover project containers/volumes
// from killed runs. Project names are deterministic (owner/repo), so an
// orphan container would collide with the next create ("name already in
// use" → 500). Only this run's slug is swept — parallel groups share the
// engine and must never touch each other's names. Best effort: docker may
// be absent when tests skip on engine health.
async function sweepOwnOrphans(): Promise<void> {
  const run = (args: string[]): Promise<string> =>
    new Promise((resolveOut) => {
      execFile('docker', args, { timeout: 30_000 }, (err, stdout) => {
        resolveOut(err ? '' : stdout)
      })
    })
  const names = (out: string): string[] => out.split('\n').map((s) => s.trim()).filter(Boolean)
  const containerRe = new RegExp(`^pcoder-e2e-${RUN_SLUG}-\\d+$`)
  const volumeRe = new RegExp(`^pcoder-e2e-${RUN_SLUG}-\\d+-(repo|home)$`)
  const orphans = names(await run(['ps', '-a', '--format', '{{.Names}}'])).filter((n) => containerRe.test(n))
  if (orphans.length > 0) {
    await run(['rm', '-f', ...orphans])
  }
  const volumes = names(await run(['volume', 'ls', '--format', '{{.Name}}'])).filter((n) => volumeRe.test(n))
  if (volumes.length > 0) {
    await run(['volume', 'rm', ...volumes])
  }
}

// Boots a per-run git daemon on the host serving tiny fixture repos, one
// per slot: git://host.docker.internal:$GIT_PORT/e2e/<run>-<slot>. Project
// containers reach it through host.docker.internal (the project image maps
// it to host-gateway) — the same pattern as the Go live-engine fixture.
//
// One repo is one project (ids are owner/repo), so slots give each test up
// to MAX_SLOTS simultaneously-live projects without ever touching github:
// hermetic, instant, and collision-free across parallel groups (each group
// has its own daemon, port, and run slug).
export const MAX_SLOTS = 8

export const PID_FILE = resolve(RUN_DIR, 'git-daemon.pid')
export const REPOS_DIR = resolve(RUN_DIR, 'git-repos')

function git(args: string[], cwd: string): void {
  execFileSync('git', args, { cwd, stdio: 'ignore' })
}

export function seedRepos(): void {
  rmSync(REPOS_DIR, { recursive: true, force: true })
  for (let slot = 1; slot <= MAX_SLOTS; slot++) {
    const dir = resolve(REPOS_DIR, 'e2e', `${RUN_SLUG}-${slot}`)
    mkdirSync(dir, { recursive: true })
    git(['init', '-q', '-b', 'main', '.'], dir)
    writeFileSync(resolve(dir, 'hello.txt'), 'hi\n')
    writeFileSync(resolve(dir, 'README.md'), `# e2e fixture ${RUN_SLUG}-${slot}\n`)
    git(['add', '-A'], dir)
    const env = {
      ...process.env,
      GIT_AUTHOR_NAME: 'e2e',
      GIT_AUTHOR_EMAIL: 'e2e@example.com',
      GIT_COMMITTER_NAME: 'e2e',
      GIT_COMMITTER_EMAIL: 'e2e@example.com',
    }
    execFileSync('git', ['commit', '-qm', 'first'], { cwd: dir, stdio: 'ignore', env })
  }
}

export function startDaemon(): void {
  mkdirSync(RUN_DIR, { recursive: true })
  seedRepos()
  const child = spawn(
    'git',
    ['daemon', `--base-path=${REPOS_DIR}`, '--export-all', '--reuseaddr', '--listen=0.0.0.0', `--port=${GIT_PORT}`],
    { detached: true, stdio: 'ignore' },
  )
  child.unref()
  if (child.pid === undefined) throw new Error('git daemon did not start')
  writeFileSync(PID_FILE, String(child.pid))
  // Fail fast when the port is squatted: a daemon that cannot bind exits,
  // and every later clone would hang or fail mysteriously mid-suite.
  const deadline = Date.now() + 15_000
  for (;;) {
    try {
      execFileSync('git', ['ls-remote', `git://127.0.0.1:${GIT_PORT}/e2e/${RUN_SLUG}-1`], { stdio: 'ignore' })
      return
    } catch {
      if (Date.now() > deadline) {
        throw new Error(`git daemon on :${GIT_PORT} never became ready — is the port squatted?`)
      }
      Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, 200)
    }
  }
}

export default async function globalSetup(): Promise<void> {
  // Best effort replaces any stale daemon+repos from a killed run: the
  // teardown below only runs when the process exits cleanly.
  try {
    stopDaemon()
  } catch {
    // nothing to stop
  }
  await sweepOwnOrphans()
  startDaemon()
}

export function stopDaemon(): void {
  // Kill by pidfile first, then by port: a SIGKILLed run never removes its
  // pidfile, and without this the stale daemon squats on GIT_PORT serving
  // a deleted repos dir while the fresh daemon fails to bind.
  try {
    const pid = Number(readFileSync(PID_FILE, 'utf8'))
    if (Number.isFinite(pid) && pid > 0) process.kill(pid, 'SIGKILL')
  } catch {
    // already gone
  }
  try {
    const out = execFileSync('lsof', ['-t', `-i:${GIT_PORT}`], { stdio: ['ignore', 'pipe', 'ignore'] })
      .toString()
    for (const line of out.split('\n')) {
      const pid = Number(line.trim())
      if (Number.isFinite(pid) && pid > 0 && pid !== process.pid) {
        try {
          process.kill(pid, 'SIGKILL')
        } catch {
          // raced teardown
        }
      }
    }
  } catch {
    // lsof missing or nothing listening
  }
  try {
    rmSync(PID_FILE, { force: true })
  } catch {
    // nothing to clean
  }
}
