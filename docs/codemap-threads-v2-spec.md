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
- `extractorOutput` RENAMED OUT: Turn drops `ExtractorOutput` entirely
  (Go field, N.json key, threadJSON output, POST response). N.json is the
  user-facing side: prompt + sections + tools + time/sha/error only.
- Adds optional `error` (NEW):

```json
{
  "turnId": "<MintID hex>",
  "sha": "<HEAD at turn start>",
  "prompt": "<full prompt, <=4000 chars>",
  "sections": null,
  "tools": null,
  "time": "RFC3339",
  "error": "<optional>"
}
```

- `N` = max(existing N) + 1, never reuse (NOT count+1).
- Placeholder written under lock BEFORE model runs: bare fields
  turnId/prompt/time/sha, sections/tools null, `error: null` explicitly
  (success keeps null; failure sets string; crash leaves placeholder
  un-overwritten which still reads as null + answer-less — see §8 for
  how FE tells crash apart from in-flight via `runningThreadId`).
- Completion OVERWRITES same `N.json` with full turn (direct `WriteFile`,
  NO tmp+rename — torn reads accepted as negligible per KISS; a reader
  that hits a half-written file treats JSON parse failure as skipped
  turn, never fatal).
- Failure fills `error` in same file; placeholder stays visible.
- Retry rewrites the LAST turn only: the highest-N `N.json` + `N.lineage.json`
  are rewritten from scratch (same turnId, same path, same prompt; fresh
  time+sha; empty events) then reruns with context turns 1..N-1 only. No
  N+1, no retry metadata. Only a failed/crashed last turn is retryable —
  a successful last turn or any non-terminal turn is never retried. From
  any angle it looks like the first attempt at N.
- Crash window (prompt, no sections, no error): UI treats as
  failed/crashed with retry hint. Accepted.
- No 200-turn cap. Keep all turns.
- Follow-up context: server reads ALL prior `N.json` in numeric order,
  skipping manifest.json + *.lineage.json. Lineage files are NEVER read
  for context — not for follow-ups, not for retry, not for history
  rebuild. Context comes exclusively from `N.json` turn files
  (prompt + sections + tools per turn). Lineage is write-only debug
  output (written on reserve/complete/fail, read only by diagnostics).

## 5. Lineage `N.lineage.json` (1 per turn, accumulate)

- Strictly 1 lineage per turn beside its turn. No project/thread lineage.
- `agent.Lineage` shape: (`initialRequest`, `turnId`, `threadId`,
  `prompt`, `time`, `events[]`, `prunedTier1Data`, `error`).
  `ExtractorInput` RENAMED to `prunedTier1Data` (the pruned snapshot
  tier-2 received). No other renames.
- Placeholder lineage at ack time: REMOVED (no lineage placeholder —
  see §6; lineage is written once at Complete/Fail).

## 5b. Two-tier pipeline (tier-1 gather → prune → tier-2 extract)

The v2 storage rewrite does NOT change this pipeline. Spec pins it so the
rewrite cannot accidentally collapse it into a single call:

1. Tier-1 (gather, agentic loop, tools ON, schema OFF): `agent.Run`
   loops up to 8 steps with `search_code` + `read_file`, history =
   prior-turn sections+tools replay + new prompt. Ends with a free-text
   final answer (grounded by construction — zero-tool answers get one
   grounding nudge; maxSteps exhaustion gets one tools-free close-out).
2. Prune (code seam `extractorSnapshot`, TUNABLE): builds
   `prunedTier1Data` = `{answer, events[]}` with CURRENT caps pinned —
   answer 16k, per-event output 6k, content 8k, args 2k, error 2k,
   llm/format payloads 12k. Covers the CURRENT turn's final answer +
   tool events only; prior-turn sections are NOT embedded (they reach
   tier-2 indirectly via tier-1's answer). Algorithm may be retuned
   behind the seam; caps are the pinned starting point.
3. Tier-2 (extract, tools OFF, schema ON): `formatResult` sends
   `prunedTier1Data` to the `structuredExtractor` tier with the codemap
   json_schema. Tier-2 uses its OWN dedicated system prompt — it MUST
   NOT share tier-1's prompt. Tier-1's prompt (codemap `systemPrompt`:
   gather/context role) and tier-2's prompt (extractor instruction:
   "Produce only JSON matching the response schema... prune chatter...
   never invent refs/paths") live as SEPARATE constants and evolve
   independently. Exactly one attempt, no retry, no schema-free
   fallback; failure aborts the turn.
4. Parse + hydrate + persist: tier-2 JSON parsed into sections, refs
   validated/hydrated with exact lines (server overwrites snippets),
   then N.json (user side: prompt/sections/tools/time/sha) +
   N.lineage.json (debug side: full events + prunedTier1Data) written
   in place over the placeholders. Raw tier-2 text lives only in
   lineage `format_response` events — never in N.json.
5. Retry reuses the last turn's path: rewrite N.json + N.lineage.json from
   scratch, context = turns 1..N-1 re-fed through steps 1–4. Retry is a
   FULL rerun (tier-1 loop + prune + tier-2, never tier-2-only); the
   lineage is rebuilt from scratch alongside the turn. Only the terminal
   (highest-N) turn may be retried, and only while failed/crashed.

- Accumulate forever; project-level mtime prune DELETED.

## 6. Store API (`codemapthreads`) — rewrite

- `Create(project, title)` DELETED. No empty threads / upfront reserve.
- `AppendTurn` reworked into Reserve + Complete + Fail (decided names:
  `ReserveTurn` / `CompleteTurn` / `FailTurn`), all under ONE global
  store mutex (existing behavior kept — single-user app, cross-project
  serialization harmless; atomicity is process-wide). NO standalone
  `SaveLineage`: v1's handler-called SaveLineage is folded in —
  ReserveTurn writes the N.json placeholder only (NO lineage
  placeholder), CompleteTurn/FailTurn write N.json + N.lineage.json
  together in place. Handlers never touch lineage files directly.
- `Rename` DELETED (+ PATCH route deleted).
- `Get(project, id)`: reassemble Thread from manifest.json + sorted N.json
  (numeric sort by file-N; skip manifest + *.lineage.json; gaps returned
  as-is in file-N order — UI renders positionally; retry is last-turn
  (highest-N) only). Folders without a valid manifest: unknown-thread (404).
  Manifest-only folders (no 1.json yet — reserve crash window): NOT
  visible; reserve writes manifest + 1.json placeholder atomically under
  one store lock, and on reserve failure the folder is removed and no
  threadId is returned (extends §11.1 past manifest), so this state
  cannot persist.
- `List(project)`: per thread dir read manifest.json (id/title/createdAt)
  + 1.json for preview (first prompt, truncate 120 — MAY diverge from
  the 60-char title on 1-line prompts >60 chars; server title wins) +
  count N.json for turnCount. createdAt from manifest; updatedAt derived
  from last turn time (== createdAt when empty). Sort strictly by
  createdAt, newest first. Skip folders without valid manifest or 1.json
  (same atomic-reserve guarantee as Get — no persistent empties).
- `Delete(project, id)`: RemoveAll(threadDir). Idempotent. validID stays.
  Same-tid delete mid-run → 409 via the existing `codemapRunning` guard
  in the handler (store Delete itself stays dumb). Cross-tab/API deletes
  bypass the tab-level UI block (§8) — accepted; an in-flight
  Complete/Fail onto a gone tree returns a detailed 500 WITH threadId,
  and any orphaned placeholder reads as crashed/retryable post-restart.
- No per-turn read API and no standalone lineage write API: handlers use
  Reserve/Complete/Fail only; diagnostics read N.lineage.json from disk
  directly (no HTTP exposure).
- Legacy flat `*.json` / `*.lineage.json` directly under the project dir:
  DELETE them all, no back-compat. One-time `sweepLegacyFlatFiles`
  called from store init/List (plus tests assert absence).
- Store mutex scope pinned: ONE global mutex (existing `s.mu` behavior,
  NOT per-project). Single-user app — cross-project serialization is
  harmless and keeps the atomic-reserve claim process-wide. Per-project
  isolation still comes from `codemapBusy` single-flight in httpapi.

## 7. Handlers (`httpapi`)

- `POST /codemap/threads` (create) DELETED. `PATCH` rename DELETED.
- `POST /codemap {prompt, threadId?}` is the append path (new thread when
  threadId empty — body-carried optional id; follow-up append otherwise).
  Status: 200 success; 502 model/prune/tier-2 fail and 504 timeout — both
  WITH `threadId`+`threadTitle` in the JSON body (same `writeCodemapErr`
  shape as today; FE `ApiError.body` carries it — the established
  new-thread AND follow-up catch path in `generate()` adopts the id and
  opens the failed placeholder; §10 pins both). 409 busy; 404 unknown
  thread; 500 pre-manifest detailed (NO threadId). NO 201 anywhere
  (implicit create returns 200).
  1. Validate prompt + AI config + repo dir (unchanged).
  2. `codemapTake` busy lock (unchanged, project single-flight).
  3. threadId empty: MintID, mkdir, manifest.json (title from prompt),
     reserve turn 1 — manifest + 1.json placeholder written ATOMICALLY
     under one store lock; on failure RemoveAll(folder) + detailed 500
     with NO threadId. Else Get-or-404, reserve max+1.
  4. ReserveTurn writes the N.json placeholder BEFORE the model runs
     (NO lineage placeholder; lineage is written once at Complete/Fail).
  5. History from prior N.json turn files ONLY (lineage NEVER read —
     §4), excluding the new placeholder; run `codemap.Ask` (pipeline
     per §5b; persist mapping per §4: drop ExtractorOutput, rename input
     to prunedTier1Data); CompleteTurn writes N.json + N.lineage.json in
     place on success, FailTurn fills `error` (+ writes N.lineage.json with the failure graph). Manifest untouched after create.
  6. Post-manifest errors keep `threadId` (+title) so FE can open the
     failed placeholder (folder always has ≥1.json by the atomic
     guarantee, so the id is always openable — D3 closed).
- NEW `POST /codemap/threads/{tid}/retry` (empty body): retries the LAST
  (highest-N) turn, which MUST be failed/crashed. Server guards: invalid
  threadId → 404; no turns yet or last turn successful (sections present,
  no error) → 409 ("retry only failed turns"); last turn in-flight
  (running) → 409. Rewrite that turn's `N.json` + `N.lineage.json` from
  scratch (same turnId/path/prompt, fresh time+sha, empty events);
  context = turns 1..N-1 only; FULL rerun (tier-1 + prune + tier-2,
  never tier-2-only). Same busy lock (any run blocks retries
  project-wide). Last turn only by construction — there are no later
  turns to go stale. Retry history is last-attempt-only (prior failures
  unrecoverable — intentional, lineage is not a retry log). Response
  mirrors `POST /codemap`: 200 on rerun success (FE reload+open), 502/504
  on rerun failure WITH `threadId`+`threadTitle` in the `writeCodemapErr`
  body (FE adopts id, opens failed placeholder); RemoveAll(folder) if the
  rerun cannot initialize the turn.
- GET threads / GET thread / DELETE thread stay (folders backend).
  `threadJSON` shape UNCHANGED (minus dropped `extractorOutput` key).
  `GET /file` unchanged.
- DELETE same-tid mid-run → 409 (existing guard kept); cross-thread /
  project `RemoveAll` mid-run → in-flight write fails as detailed 500.
  No server-side delete block beyond same-tid 409; tab-level UI block
  (§8) narrows the window but does not close cross-tab/API deletes.
- `GET threads` gains `runningThreadId: string|null` (from the existing
  in-memory `codemapRunning(id)` — NO new endpoint, NO store read: the
  handler already holds the busy map; it just surfaces the value). This
  is HOW the UI knows on remount that a generation is mid-flight (see
  §8). Server restart clears it (in-memory) — a placeholder left
  answer-less across a restart reads as failed/crashed, retryable.

## 8. Frontend (`CodemapTab.tsx`)

- New chat stays LOCAL-ONLY (activeId null) until first Send. No reserve.
- `generate()` POSTs `{prompt, threadId: activeId ?? undefined}`; on
  success adopts threadId + threadTitle, reloads + opens. On failure
  with threadId: adopts id, reloads, opens failed placeholder.
- FE adopts the typed prompt as optimistic title immediately, reconciles
  with server threadTitle on response (server truncation wins).
- Composer stays cleared on failure (decided — retry is a button, not a
  resend from the composer).
- Retry button on the LAST FAILED turn only (not on success, not on
  non-terminal turns). Failed hint text under the turn; retry calls
  `POST /codemap/threads/{tid}/retry` and reuses `generate()`'s response
  handling: on 200 reload+open, on 502/504 adopt threadId+threadTitle and
  open the failed placeholder.
- History drawer unchanged (title/turnCount/preview/time). No rename UI.
- Failed turns (error string, no sections): failed bubble + hint + retry
  button. Answer-less + `error: null`: in-flight IF its thread ==
  `runningThreadId`, else crashed/failed (retryable only when it is the
  last turn). The project-wide busy flag alone CANNOT disambiguate
  cross-thread — `runningThreadId` is the per-turn signal.
- Mid-generation UI block (codemap tab only; other tabs free): while
  `busy` (own POST in flight) OR the open thread == `runningThreadId`
  (remounted into someone else's run), the composer + history-delete +
  retry buttons disable. Unblocks when the run's thread re-GETs with
  filled sections / error.
- Remount-into-run (THE ANSWER to "how does the UI know"): `CodemapTab`
  mounts → `GET threads` → reads `runningThreadId` → if non-null, opens
  that thread, sets `busy=true`, shows the spinner on the answer-less
  placeholder turn. No new endpoint: the list response already carries
  it. Poll `GET threads` (or the open thread) on mount only — no live
  subscription. Completion is observed when the placeholder gains
  sections/`error` (next poll / user revisit); the in-flight POST owner
  still gets its direct response.
- `initialRequest` lineage shape pinned: `{userPrompt, model}` only
  (loop.go today) — no system prompt, no snapshot.
- Tier-1 step accounting: grounding nudge + maxSteps close-out consume
  steps of the 8; caps (§5b.2) apply post-nudge to whatever lands in
  `prunedTier1Data`.

## 9. Priority: UX > simplicity > efficiency

- List reads manifest + 1.json + count per thread. Get reads all N.json.
  No caching. Files are small; human-browsable.

## 10. Tests to rewrite

- `codemapthreads_test.go`: folders, max+1, placeholder overwrite,
  error fill, legacy sweep, numeric sort past 9. Turn has no
  ExtractorOutput; lineage asserts `prunedTier1Data`, never
  extractorInput/extractorOutput keys.
- `codemap_threads_test.go`: CRUD minus create/rename.
- `codemap_history_test.go`: replay, null-choices placeholder asserts.
- `codemap_project_delete_test.go`: folder asserts, not prompt.json paths.
- `codemap_test.go`: per-turn file asserts.
- Retry endpoint tests: retries LAST failed turn only, keeps turnId/path,
  context 1..N-1; 409 on success / non-terminal / in-flight; 404 on
  unknown thread/empty; busy contention with codemap POST; 502/504 on
  rerun failure WITH threadId (FE adopts id + opens placeholder); 200
  rerun success reloads+opens (mirrors generate()).
- Pipeline tests: tier-1 loop carries no schema; tier-2 format call
  carries schema, no tools, exactly once; `extractorSnapshot` caps stay
  pinned (answer 16k / output 6k / content 8k / args 2k / error 2k /
  payloads 12k); lineage `prunedTier1Data` round-trips the snapshot.
- Status-code tests: 200 create+append+retry; 502/504 run fail WITH
  threadId; 500 pre-manifest detailed NO threadId; 409 busy +
  retry-not-failed + retry-non-terminal; 404 unknown thread/empty.
- `runningThreadId` tests: set while POST in flight, null when idle,
  cleared on server restart (in-memory); FE remount opens it with busy.
- e2e POST-threads 201 mock rewritten to implicit flow (all 200s).
- REQUIRED e2e (remount-into-run, route-mocked, no engine/model):
  1. Mock `GET threads` → `{threads: [T], runningThreadId: "T"}` and
     `GET threads/T` → thread with answer-less placeholder turn
     (`error: null`, no sections).
  2. Goto terminal codemap tab → spinner shows on the placeholder,
     composer + delete + retry disabled (UI block).
  3. Re-mock `GET threads/T` with filled sections; revisit/remount →
     spinner gone, sections render, controls re-enable.
  4. Crash variant: `runningThreadId: null` + answer-less placeholder →
     renders failed/crashed with retry hint (NOT spinner), retry button
     enabled.
  This pins the whole §8 contract: busy restore, block scope,
  crash-vs-in-flight via `runningThreadId`, and no-live-subscription
  (mount-poll only).

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
6. FE type `CodemapTurn`: drop `extractorOutput` (N.json user side no
   longer carries it); web `types.ts` + `CodemapTab` updated.

