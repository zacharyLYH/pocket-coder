---
name: playwright-route-mocks
description: Route-mocked Playwright specs against the real TerminalView without a Docker engine. Use when testing terminal-tab UI with canned API data.
---

# Playwright Route Mocks (terminal tabs)

## Glob gotcha
In `page.route` patterns `*` does NOT cross `/` but `**` does.
A project id is `%2F`-encoded (no literal slash), so:

- `**/api/projects/*/observe*` matches `/observe?...` but NOT `/observe/stats`
- use `**/api/projects/*/observe**` to also match `/observe/stats` and `/observe/errors`

Symptom of the bug: the tail works but stats/errors show live-backend values.

## Pattern
```ts
// e2e/<area>.fixtures.ts: canned logs + mock installer
await page.route('**/api/ai/config', (r) => r.fulfill({ json: { configured: false } }))
await page.route('**/api/projects/*/sessions*', (r) => r.fulfill({ json: { sessions: [{ name: 'main' }] } }))
await page.route('**/api/projects/*/observe**', (r) => {
  const u = new URL(r.request().url()) // NOTE: r.request().url(), not r.url()
  if (u.pathname.endsWith('/observe/stats')) return r.fulfill({ json: STATS })
  if (u.pathname.endsWith('/observe/errors')) return r.fulfill({ json: GROUPS })
  // ... filter canned logs by level/source/type/trace/q + after/before/limit
})
await page.goto(`/projects/${encodeURIComponent(id)}/terminal/main`)
await page.getByTestId('tab-<name>').click()
```

No `engineUp` gate needed (no engine touched), but the specs still run
inside the standard stack, so auth `storageState` applies. Grant
`clipboard-read`/`clipboard-write` in `beforeEach` when testing Copy buttons.
