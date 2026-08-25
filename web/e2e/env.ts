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

// RUN_DIR namespaces every on-disk artifact of one run.
export const RUN_DIR = process.env.E2E_RUN_ID
  ? `test-results/${process.env.E2E_RUN_ID}`
  : 'test-results'

export const DATA_DIR = `${RUN_DIR}/sps-stack-data`
export const SERVER_LOG = `${RUN_DIR}/sps-stack-server.log`
export const AUTH_STATE = `${RUN_DIR}/auth-state.json`
