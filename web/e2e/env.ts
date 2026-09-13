// Run-scope plumbing shared by playwright.config.ts and the specs.
//
// The suite runs either as ONE process (defaults: ports 8081/5174, files
// under test-results/) or as several PARALLEL processes, one per test type
// (see e2e-parallel.sh). Each process gets its own ports AND its own
// test-results subdirectory (E2E_RUN_ID) because everything under it —
// the wiped data dir, the server log the PIN is read from, the saved auth
// state — is per-run state that parallel processes must not share.

function intEnv(name: string, def: number): number {
  const v = parseInt(process.env[name] ?? '', 10)
  return Number.isFinite(v) && v > 0 ? v : def
}

export const API_PORT = intEnv('E2E_API_PORT', 8081)
export const WEB_PORT = intEnv('E2E_WEB_PORT', 5174)

// Host port of the per-run git daemon (see global-setup.ts). Each parallel
// group gets its own daemon + port via E2E_GIT_PORT, so fixture repo URLs
// — and therefore project ids — never collide across groups sharing one
// Docker engine.
export const GIT_PORT = intEnv('E2E_GIT_PORT', 8091)

// Filesystem slug of this run, used to namespace fixture repo owners.
export const RUN_SLUG = (process.env.E2E_RUN_ID ?? 'default').replace(/[^a-zA-Z0-9_-]/g, '-')

// RUN_DIR namespaces every on-disk artifact of one run.
export const RUN_DIR = process.env.E2E_RUN_ID
  ? `test-results/${process.env.E2E_RUN_ID}`
  : 'test-results'

export const DATA_DIR = `${RUN_DIR}/pcoder-stack-data`
export const SERVER_LOG = `${RUN_DIR}/pcoder-stack-server.log`
export const AUTH_STATE = `${RUN_DIR}/auth-state.json`

// state.json fixture copied into the data dir before the server boots.
// E2E_SEED=bootstrap.seed.json (bootstrap group) starts the stack from a
// pre-existing project so the boot-bootstrap journey can be tested from an
// empty Docker state; the default seed is a fresh user with no projects.
//
// Auto-detects: if the command-line filters include the bootstrap spec
// (e.g. `npx playwright test e2e/bootstrap.spec.ts`), default to the
// bootstrap seed so the test works standalone without `e2e-parallel.sh`.
const argv = process.argv.slice(2).join(' ')
const isBootstrapRun = /bootstrap\.spec/.test(argv) || process.env.E2E_SEED === 'bootstrap.seed.json'
export const SEED_FILE = process.env.E2E_SEED ?? (isBootstrapRun ? 'bootstrap.seed.json' : 'state.seed.json')

// Compose project name for this run's backend stack. Single source of truth
// shared by playwright.config.ts (webServer up) and global-teardown.ts
// (deterministic down even if the webServer's EXIT trap never fires).
// Run ids like "preview.a" are sanitized: compose v2 rejects dots (and most
// punctuation) in project names, which is why the three preview groups used
// to fail their webServer startup outright.
export const COMPOSE_PROJECT = `pcoder-e2e-${(process.env.E2E_RUN_ID ?? 'default').replace(/[^a-zA-Z0-9_-]/g, '-')}`
