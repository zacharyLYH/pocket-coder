# Codemap threads v2 — folder-per-thread spec (DRAFT, plan only)

Status: planning. No code changes yet. Captures everything grilled/aligned
so a fresh session can implement without re-asking.

## 1. Problem with v1

- `data/codemaps/<escaped-project>/<title>.json` holds ALL turns in one
  `turns[]` array. Follow-ups append into the same file.
- Title renames move the file; lineage sits beside it as
  `<title>.lineage.json` (latest-turn-only, overwritten each turn).
- Bugs observed: lineage orphaned under turnId name
  (`f86d....lineage.json`), rename races, one giant file per thread,
  no per-turn ack.

## 2. Agreed v2 layout (slug-free)

```text
data/codemaps/<escaped-project>/<threadID>/
  manifest.json
  1.json
  1.lineage.json
  2.json
  2.lineage.json
```

- `<threadID>` = opaque stable UUID (`MintID()` hex). Folder name IS the ID.
- No prompt text / slug in ANY filename. Only `N.json` + `N.lineage.json`.
- No title-derived folders/files. No ` (1)` disambiguation.
- Legacy flat files (`<title>.json`, `<title>.lineage.json` directly under
  the project dir): actively DELETE (one-time reset accepted, non-prod).
- `DeleteProjectDir` cascade unchanged: `RemoveAll(projectDir)`.
- Manifest filename: `manifest.json` (decided).

## 3. Manifest `manifest.json` (thread-level only)

```json
{
  "id": "<threadID, == folder name>",
  "title": "<TitleFromPrompt(first prompt): first line, 60 chars>",
  "createdAt": "RFC3339"
}
```

- `id` duplicates folder name (cheap, stable; folder authoritative).
- NO `project` field (decided — parent dir is the scope).
- NO `updatedAt`, NO `nextTurnIndex`, `turnCount`, `preview`.
- Manifest is exactly `{id, title, createdAt}`.
- `title` set once at create from first prompt; rename DROPPED.
- Manifest NEVER bumps on turns (strictly thread-level). No `updatedAt`.
- List sorts strictly by `createdAt` (follow-ups never reorder).
- Summary keeps `createdAt` + `updatedAt` API fields: `updatedAt` is
  derived from the last turn file's time (or == createdAt when empty);
  `createdAt` == manifest.createdAt. FE shape unchanged.
- UI title = `manifest.title` (server-truncated 60).

## 4. Turn files `N.json` (pure Turn + error)

- Pure `Turn` only. NO duplicated threadId/project header.
- Adds optional `error` (NEW):

```json
{
  "turnId": "<MintID hex>",
  "sha": "<HEAD at turn start>",
  "prompt": "<full prompt, <=4000 chars>",
  "sections": null,
  "tools": null,
  "extractorOutput": null,
  "time": "RFC3339",
  "error": "<optional>"
}
```

- `N` = max(existing N) + 1, never reuse (NOT count+1).
- Placeholder written under lock BEFORE model runs: bare fields
  turnId/prompt/time/sha, sections/tools null.
- Completion OVERWRITES same `N.json` with full turn.
- Failure fills `error` in same file; placeholder stays visible.
- Retry of turn K rewrites the SAME `K.json` + `K.lineage.json` from
  scratch (same turnId, same path, same prompt; fresh time+sha; empty
  events) then reruns with context turns 1..K-1 only. No N+1, no retry
  metadata. From any angle it looks like the first attempt at K.
- Crash window (prompt, no sections, no error): UI treats as
  failed/crashed with retry hint. Accepted.
- No 200-turn cap. Keep all turns.
- Follow-up context: server reads ALL prior `N.json` numeric order,
  skipping manifest.json + *.lineage.json.

## 5. Lineage `N.lineage.json` (1 per turn, accumulate)

- Strictly 1 lineage per turn beside its turn. No project/thread lineage.
- Keep CURRENT `agent.Lineage` shape (`initialRequest`, `turnId`,
  `threadId`, `prompt`, `time`, `events[]`, `extractorInput`, `error`).
- Placeholder lineage at ack time (initialRequest + ids + empty events),
  overwritten in place on completion.
- Accumulate forever; project-level mtime prune DELETED.
- Internal/debug only (structured-outputs + debugging). No public/HTTP
  use; FE thread endpoints never serve lineage.

## 6. Store API (`codemapthreads`) — rewrite

- `Create(project, title)` DELETED. No empty threads / upfront reserve.
- `AppendTurn` reworked into Reserve + Complete + Fail (decided names:
  `ReserveTurn` / `CompleteTurn` / `FailTurn`), all under store lock.
- `Rename` DELETED (+ PATCH route deleted).
- `Get(project, id)`: reassemble Thread from manifest.json + sorted N.json
  (numeric sort; skip manifest + *.lineage.json). Folders without a valid
  manifest or without 1.json: unknown-thread (404).
- `List(project)`: per thread dir read manifest.json (id/title/createdAt)
  + 1.json for preview (first prompt, truncate 120) + count N.json for
  turnCount. createdAt from manifest; updatedAt derived from last turn
  time (== createdAt when empty). Sort strictly by createdAt, newest
  first. Skip folders without valid manifest or 1.json.
- `Delete(project, id)`: RemoveAll(threadDir). Idempotent. validID stays.
- `SaveLineage(project, threadID, N, raw)` / internal per-turn read:
  paths `<threadDir>/N.lineage.json`. No prune, no HTTP exposure.
- Legacy flat `*.json` / `*.lineage.json` directly under the project dir:
  DELETE them all, no back-compat. One-time `sweepLegacyFlatFiles`
  called from store init/List (plus tests assert absence).
- Store mutex guards reserve (next-index + placeholder writes atomic).
  Per-project `codemapBusy` single-flight stays in httpapi.

## 7. Handlers (`httpapi`)

- `POST /codemap/threads` (create) DELETED. `PATCH` rename DELETED.
- `POST /codemap {prompt, threadId?}` is the append path (new thread when
  threadId empty; follow-up append otherwise):
  1. Validate prompt + AI config + repo dir (unchanged).
  2. `codemapTake` busy lock (unchanged, project single-flight).
  3. threadId empty: MintID, mkdir, manifest.json (title from prompt),
     reserve turn 1. Else Get-or-404, reserve max+1.
  4. Write placeholder N.json + N.lineage.json BEFORE model runs.
  5. History from prior turn files (exclude new placeholder),
     run `codemap.Ask` (unchanged), overwrite N.json + N.lineage.json
     in place; fill `error` on failure. Manifest untouched after create.
  6. Errors keep `threadId` (+title) so FE can open the thread.
- NEW `POST /codemap/threads/{tid}/turns/{n}/retry` (empty body):
  prompt from stored N.json; rewrite K.json + K.lineage.json from
  scratch (same turnId/path/prompt, fresh time+sha, empty events);
  context = turns 1..K-1 only; rerun model; fill in place. Same busy
  lock (any run blocks retries project-wide). Retry button on failed
  turns ONLY (no retry on success). Later turns untouched.
- GET threads / GET thread / DELETE thread stay (folders backend).
  `threadJSON` shape UNCHANGED. `GET /file` unchanged.

## 8. Frontend (`CodemapTab.tsx`)

- New chat stays LOCAL-ONLY (activeId null) until first Send. No reserve.
- `generate()` POSTs `{prompt, threadId: activeId ?? undefined}`; on
  success adopts threadId + threadTitle, reloads + opens. On failure
  with threadId: adopts id, reloads, opens failed placeholder.
- FE adopts the typed prompt as optimistic title immediately, reconciles
  with server threadTitle on response (server truncation wins).
- Composer stays cleared on failure (decided — retry is a button, not a
  resend from the composer).
- Retry button on FAILED turns only (not on success). Failed hint text
  under the turn; retry calls the retry endpoint (context = turns above
  only by construction). Later turns untouched.
- History drawer unchanged (title/turnCount/preview/time). No rename UI.
- Failed turns (error, no sections): failed bubble + hint. In-flight =
  busy flag; answer-less + !busy = crashed/failed.

## 9. Priority: UX > simplicity > efficiency

- List reads manifest + 1.json + count per thread. Get reads all N.json.
  No caching. Files are small; human-browsable.

## 10. Tests to rewrite

- `codemapthreads_test.go`: folders, max+1, placeholder overwrite,
  error fill, legacy sweep, numeric sort past 9.
- `codemap_threads_test.go`: CRUD minus create/rename.
- `codemap_history_test.go`: replay, null-choices placeholder asserts.
- `codemap_project_delete_test.go`: folder asserts, not prompt.json paths.
- `codemap_test.go`: per-turn file asserts.
- Retry endpoint tests: retry failed K keeps turnId/path, context 1..K-1,
  later turns untouched, busy contention with codemap POST.
- e2e POST-threads 201 mock rewritten to implicit flow.

## 11. Remaining open questions

11. RESOLVED: pre-manifest failure → detailed 500 (`create thread: <cause>`,
    stage field), NO threadId. FE stays local; user retries as new chat.
12. RESOLVED: non-numeric `*.json` in thread dir (besides manifest.json)
    ignored by Get/List.
13. RESOLVED: keep hex-only `validID` on folder names + threadId params
    (MintID output passes; traversal blocked).

## 12. Order

1. codemapthreads rewrite + unit tests.
2. httpapi rewiring + handler tests.
3. CodemapTab failure UI + e2e mocks.
4. rfc-codemap.md History rewrite.
5. Manual: turn + follow-up + failure + delete + project delete.

- Debug only; `ReadLineage` becomes per-turn — OPEN Q4.

