import { execFileSync } from 'node:child_process'
import { resolve } from 'node:path'

import { COMPOSE_PROJECT, DATA_DIR } from './env'

// Runs after every test in the process, even when tests fail: takes down
// this run's backend stack. The webServer's own EXIT trap is not reliable
// (Playwright may SIGKILL the shell before `compose down` finishes), which
// used to leave `pcoder-e2e-<run>-server-1` containers behind on the engine.
export default function globalTeardown(): void {
  try {
    execFileSync(
      'docker',
      ['compose', '-f', '../docker-compose.e2e.yml', '-p', COMPOSE_PROJECT, 'down'],
      {
        stdio: 'ignore',
        timeout: 60_000,
        // Absolute data dir: compose resolves relative bind sources against
        // the compose file's directory and rejects missing ones outright.
        env: { ...process.env, PCODER_E2E_DATA_DIR: resolve(DATA_DIR), PCODER_E2E_API_PORT: '1' },
      },
    )
  } catch {
    // Best effort: a failed teardown must not fail the run. Leftovers are
    // visible via `docker ps` and removable with `make nuke`.
  }
}
