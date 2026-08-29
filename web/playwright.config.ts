import { defineConfig } from '@playwright/test'

import { API_PORT, AUTH_STATE, DATA_DIR, RUN_DIR, SERVER_LOG, WEB_PORT } from './e2e/env'

// One real backend for every test. Each Playwright process boots its own Go
// server + Vite dev server as webServers (needs the Go toolchain + a running
// Docker engine) and every spec — behavioral, visual, and full-stack
// journeys — exercises them with NOTHING mocked. The PIN comes from the
// console-mailer output redirected into $RUN_DIR/sps-stack-server.log.
//
// Determinism comes from two resets:
//   1. Per run: the stack data dir is wiped before the server boots, so
//      projects/keys/events from a previous run can never leak into this
//      one's screenshots.
//   2. Per test: specs that create state delete it again in `finally`
//      (deleteAllProjects / deleteAllSSHKeys in e2e/helpers.ts), so test
//      order within a run does not matter.
//
// Login happens once per process in the `login` project (auth.setup.ts);
// every other test reuses the saved session via storageState. Tests that
// need the logged-OUT screen opt out with an empty storageState.
//
// Workers are pinned to 1 within a process: the process's backend is
// shared, and parallel tests would race on the projects/keys lists (and on
// screenshots of them). PARALLELISM comes from running several processes,
// one per test type — see e2e-parallel.sh, which gives each its own ports
// and run dir via E2E_API_PORT / E2E_WEB_PORT / E2E_RUN_ID.
//
// The stack is self-contained on dedicated ports (8081 backend, 5174 web by
// default): it must NEVER run against the dev processes on 8080/5173 — the
// tests wipe projects and keys, which would destroy real data.
// reuseExistingServer is false for both servers for the same reason.
//
  // Leftover projects from a crashed run are NOT auto-removed (a name-based
// docker cleanup could destroy real projects on a shared engine). If a run
// is killed mid-test, remove strays by hand:
//   docker rm -f $(docker ps -aq --filter name=sps-) && \
//     docker volume rm $(docker volume ls -q --filter name=sps-)
export default defineConfig({
  testDir: './e2e',
  fullyParallel: true,
  workers: 1,
  outputDir: RUN_DIR,
  // Generous default: journeys do synchronous project-image builds and real
  // npm installs, and e2e-parallel.sh stacks FOUR groups (each with its own
  // Go server, Vite, and browser) on one machine — an unrelated group's
  // docker build can stall another group's API calls past the 30s default.
  // Specs needing more (the OpenCode TUI journey) raise their own budget.
  timeout: 180_000,
  // One baseline per shot, no browser/platform suffix — the same PNGs serve
  // local macOS runs and Linux CI. maxDiffPixelRatio absorbs cross-OS font
  // rasterization noise; the visual suite guards responsive layout, not
  // pixel-perfect fonts.
  snapshotPathTemplate: '{testDir}/{testFilePath}-snapshots/{arg}{ext}',
  expect: {
    toHaveScreenshot: {
      maxDiffPixelRatio: 0.05,
      animations: 'disabled',
    },
  },
  use: {
    // Locally use the installed Chrome (no browser download); in CI use
    // Playwright's bundled Chromium (npx playwright install chromium).
    channel: process.env.CI ? 'chromium' : 'chrome',
    baseURL: `http://localhost:${WEB_PORT}`,
    storageState: AUTH_STATE,
  },
  projects: [
    {
      name: 'login',
      testMatch: /auth\.setup\.ts/,
      use: { storageState: { cookies: [], origins: [] } },
    },
    {
      name: 'app',
      testIgnore: /auth\.setup\.ts/,
      dependencies: ['login'],
    },
  ],
  webServer: [
    {
      command:
        // fresh backend state every run — the data dir is e2e-only
        `rm -rf ${DATA_DIR} ${SERVER_LOG} && mkdir -p ${DATA_DIR} && ` +
        `env SPS_BIND=127.0.0.1:${API_PORT} ` +
        `SPS_LOGIN_EMAIL=me@example.com SPS_DATA_DIR=$PWD/${DATA_DIR} ` +
        // empty-but-present shadows the repo-root .env, forcing the
        // console mailer so tests can read the PIN from the log
        `SMTP_USER= SMTP_PASSWORD= ` +
        `go -C ../server run ./cmd/server 2> ${SERVER_LOG}`,
      url: `http://localhost:${API_PORT}/health`,
      reuseExistingServer: false,
      timeout: 60_000,
      stdout: 'ignore' as const,
      stderr: 'pipe' as const,
    },
    {
      command: `SPS_SERVER_URL=http://127.0.0.1:${API_PORT} npm run dev -- --port ${WEB_PORT} --strictPort`,
      url: `http://localhost:${WEB_PORT}`,
      reuseExistingServer: false,
      timeout: 60_000,
    },
  ],
})
