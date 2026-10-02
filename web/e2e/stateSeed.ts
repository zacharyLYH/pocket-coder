import { readFileSync } from 'node:fs'
import { dirname, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

import { LOGIN_EMAIL, type SeedKind } from './env.ts'

// The state seeder factory: every state.json the test suite boots from is
// DERIVED here from one canonical file — test/state.mock.json, the same
// seed `make dev-seed` copies for local dev. Kinds add only what they own,
// so no seed block is ever hand-copied, and the invariant holds by
// construction: a state that owns projects owns the server deploy key
// (the ssh gate in front of Home assumes exactly that).
//
// Run directly to print a seed — this is how playwright.config.ts writes
// state.json into the data dir before the backend boots:
//   node e2e/stateSeed.ts [default|bootstrap]

type ServerKey = { privateKey: string; publicKey: string; fingerprint: string; createdAt: string }
type Harness = { id: string; name: string; command: string; install?: string }
export type CanonicalMock = {
  user: { email: string }
  serverKey: ServerKey
  harnesses: Record<string, Harness>
  projects: Record<string, unknown>
}

// canonicalMock reads the one true seed. Resolved from this file (not
// cwd), so the CLI works from anywhere — and built with path math, not
// `new URL(..., import.meta.url)`, which Vite rewrites as an asset URL.
export function canonicalMock(): CanonicalMock {
  const here = dirname(fileURLToPath(import.meta.url))
  return JSON.parse(readFileSync(resolve(here, '../../test/state.mock.json'), 'utf8')) as CanonicalMock
}

// seedState builds the boot state for one kind. 'default' is a fresh
// stack (user + deploy key, no projects); 'bootstrap' starts from a
// pre-existing project so boot-bootstrap can be tested from an empty
// Docker state.
export function seedState(kind: SeedKind): Record<string, unknown> {
  const mock = canonicalMock()
  if (!mock.serverKey?.privateKey) {
    throw new Error('test/state.mock.json lost its serverKey — a state seed must own the deploy key')
  }
  const base = { user: { email: LOGIN_EMAIL }, serverKey: mock.serverKey }
  switch (kind) {
    case 'default':
      return base
    case 'bootstrap':
      return {
        ...base,
        harnesses: { opencode: mock.harnesses.opencode },
        projects: {
          // repo is empty on purpose: this journey is about the boot pass
          // (container + harness install), not cloning.
          e2eboost: {
            repo: '',
            harnesses: ['opencode'],
            sessions: { oc1: { harness: 'opencode' } },
          },
        },
      }
    default:
      throw new Error(`unknown seed kind ${JSON.stringify(kind)}`)
  }
}

// CLI: print one kind as the state.json the backend should boot from.
if (process.argv[1] !== undefined && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const kind = (process.argv[2] ?? 'default') as SeedKind
  process.stdout.write(`${JSON.stringify(seedState(kind), null, 2)}\n`)
}
