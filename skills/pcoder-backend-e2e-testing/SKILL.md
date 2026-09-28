---
name: pcoder-backend-e2e-testing
description: How to boot the Pocket Coder Go server for live agentic testing, authenticate with the pcoder_session cookie, exercise butler/codemap against the real AI model, verify persisted JSON logs (thread turns, lineage, approvals.json), and clean up. Use when running end-to-end backend tests, debugging butler or codemap behavior against real models, or verifying agent features without touching the FE.
---

# Pocket Coder Backend E2E Testing

How to boot the app, hit it live, verify the JSON logs, and get out clean. Everything here was learned by hitting the sharp edges for real.

## Layout in one screen

```
server/                 Go control plane (this is what you run)
  cmd/server/main.go    boot only; HTTP surface in internal/httpapi
  internal/
    agent/              L0 turn engine (Pipeline, loop, history builder)
    threads/            L2 store: butler/<tid>/ + codemaps/<project>/<tid>/
    httpapi/            butler + codemap products: tools, confirms, handlers
    prompt/             guide text per product
  data/                 LIVE state (state.json, butler/, codemaps/, events.log)
web/                    React FE (vite dev proxies /api + /ws to :8080)
```

Three-shelf mental model: **engine** (`agent`), **memory** (`threads`), **products** (`butler`, `codemap` — each = scope prompt + tool registry + payload type + one distinctive behavior). If a test needs a new knob, it usually belongs on one of those shelves, not in the handler.

## Booting a test server

```bash
cd server
go build -o /tmp/pcoder-test ./cmd/server
PCODER_BIND=127.0.0.1:8977 /tmp/pcoder-test > /tmp/pcoder-test.log 2>&1 &
```

Config is env-only (`internal/config`), `PCODER_`-prefixed; a repo-root `.env` is also loaded. Defaults are fine for local testing. `PCODER_DATA_DIR` defaults to `./data` — **it is relative to cwd**, so always `cd server` first or you get a fresh empty state in the wrong directory.

Wait for readiness by probing a real route, not a sleep loop against `/`:

```bash
for i in $(seq 1 20); do
  code=$(curl -s -o /dev/null -w "%{http_code}" "http://127.0.0.1:8977/api/projects" -b "pcoder_session=$TOKEN")
  [ "$code" != "000" ] && break; sleep 0.5
done
```

## Authentication — the #1 sharp edge

Auth is an **HttpOnly cookie** (`pcoder_session`, see `internal/auth/auth.go:36`), NOT an `Authorization: Bearer` header. `Authorization: Bearer ...` silently 401s every route.

```bash
TOKEN="<JWT from your browser's pcoder_session cookie>"
curl -b "pcoder_session=$TOKEN" http://127.0.0.1:8977/api/projects
```

Getting a token if you don't have one: POST `/api/auth/request-pin` with `{"email":"<state.json user.email>"}`. Without SMTP configured, the PIN prints to the server's **stderr** (ConsoleMailer) — read it from the server log. Then POST `/api/auth/verify` `{"email","pin"}`; the response sets the cookie.

Other things that 404/401 confusingly:
- `/` has no route. A bare GET returns Go's `404 page not found` — that does NOT mean the server is dead. Probe `/api/projects`.
- Codemap is project-scoped: `POST /api/projects/{id}/codemap` where `{id}` is `owner/repo` **URL-encoded** (`developit%2Fpreact-vite-template`). Butler is global: `POST /api/butler/turn`.
- The sandbox/background-process gotcha: if your environment reaps background processes between tool calls, run the **entire experiment (boot → turns → log checks → kill) inside one script invocation**. A server started in one call is gone by the next.

## Pre-flight checklist

1. **Docker reachable** — butler reads container stats live; codemap runs repo scans in containers. `docker ps` first.
2. **AI model configured** — check `server/data/state.json` → `ai_models[]` (has an entry with an `apiKey`/baseURL). Without it, butler turns fail with "ai not configured" (turn still persists as a failed, retryable turn — by design).
3. **A project exists** — `GET /api/projects` with the cookie.
4. **Old test servers not squatting on your port** — `lsof -ti :8977`.

## Running the agents

Butler (SSE stream, one turn ≈ 10–30s on a small model):

```bash
curl -sN --max-time 120 -X POST "$B/butler/turn" -b "pcoder_session=$TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"prompt":"Start the preview for <owner/repo> on port 3000."}'
```

The last SSE `data:` line is the final JSON: `threadId`, `answer`, `steps[]`, and `confirms[]` for write proposals. Codemap is a plain JSON POST (batch, up to 10 min timeout).

**To exercise the approval flow, ask for something that mutates** ("start the preview…", "stop project X"). Reads answer in text; writes return confirm cards. Apply one:

```bash
curl -X POST "$B/butler/confirms/$CID/apply" -b "pcoder_session=$TOKEN" -H "Content-Type: application/json" -d '{}'
# or /discard
```

## Verifying the JSON logs (the actual point)

Everything lands under `server/data/`:

| What | Where | What "good" looks like |
|---|---|---|
| Turn transcript | `butler/<tid>/<N>.json` | `payload.answer` non-empty, `payload.steps[]` each `{tool, args, result\|error}`, `error: null` on success |
| Full LLM trace | `butler/<tid>/<N>.lineage.json` | `events[]` alternating `llm_request`/`llm_response` (+ `tool_call` entries); first request carries the scope-gate (`butler_scope`) call |
| Approvals | `butler/<tid>/approvals.json` | proposed row `status:"pending"` → after apply `status:"approved"` + `resolvedAt` set (or `"discarded"`) |
| Codemap turn | `codemaps/<project>/<tid>/<N>.json` | `payload.sections[]` with title/summary/refs |
| Global event log | `data/events.log` | one `butler.turn`/`codemap.turn` line per completed turn, `butler.apply` per apply |
| Per-project obs | `data/observe/<project>.log.jsonl` | structured request logs |

Butler is propose-only by construction: a live turn can never mutate anything. Worst case of replaying anything is a stale confirm card, which a human reviews anyway — that's why retry is safe for butler too (`POST /api/butler/threads/{tid}/retry`, 409s unless the last turn failed).

## Known sharp edges in the product itself

- **Duplicate proposals used to stack**: the 2.6B model calls `preview_start` repeatedly → N identical pending approvals. Since the dedupe fix, `AddApproval` returns the existing pending id for identical tool+args, and resolving one discards its identical pending duplicates (`internal/threads/threads.go`). If you see duplicates again, check `approvalKey` normalization.
- **A pending approval blocks follow-ups** (`ErrPendingApproval` → 409 "pending confirmation"). Resolve the card before sending another message in that thread. This is a guard, not a bug.
- **Failed butler turns are features**: the turn persists with `error` set and stays retryable; nothing 500s. Check `<N>.json` `.error` before assuming the endpoint broke.
- **Scope gate costs one extra LLM call** at the start of every butler/codemap turn (structured `can_help`/`about_code` boolean, fails open). In lineage it's the first `llm_request` — don't mistake it for a duplicated main call.
- **`data/` is live state, not fixtures.** The 958 diagnosis thread etc. live here. `rm -rf data/butler data/codemaps` is a legit dev reset (user approved treating them as disposable) — but never delete `data/state.json` (models, keys, identities) or `data/jwt-secret`.

## Cleanup — always

```bash
pkill -f pcoder-test; sleep 1
lsof -ti :8977 | xargs kill -9 2>/dev/null   # kill survives if pkill missed the port holder
curl -s -o /dev/null --max-time 2 http://127.0.0.1:8977/api/projects && echo "STILL UP" || echo "down"
```

Verify the process count is 0 (`ps aux | grep [p]coder`). Leaving a test server bound to the data dir while the real one boots is how you get mysterious empty-state behavior.

## Test hygiene

- Unit tests first: `go test ./internal/... -count=1` (18 packages, ~40s; httpapi is the slow one). Vet clean too: `go build ./... && go vet ./internal/...`.
- Tests script fake OpenAI-compatible models (`newFakeModel` in `butler_test.go` / `codemap_fake_test.go`). **Every butler/codemap fake-model sequence must open with a scope-allow call** (`scopeAllow` / `codeAllow`) — the gate is a real call the scripts must answer, and call counts in assertions shift by one per turn.
- Only go live after units are green; the live run is for verifying *wiring* (auth, docker, state, persistence), not logic.
