# Git tab for mobile — RFC (DRAFT, plan only)

Status: implemented (v1; §3 checks and §1.1 review state deferred to v2
as the doc prescribes). Post-review deltas: no Create-PR surface (push
ships the branch; only the AI-drafted description stays, copy-only), no
recent-message chips (`GET /git/log` removed), and Commit/Push are two
separate buttons instead of one contextual action.

Scope: the Diff tab becomes the **Git** tab and gains commit, AI
helpers, checks, push/pull/PR, and branch switching. One git surface
only (staging already lives there). The diff reader adopts
collapsed-context hunks with progressive expansion, file-to-file flow,
and mark-as-reviewed. Quick keys (modifier strip) ships as an
independent side feature. Out of scope: watch mode, merge-conflict UI,
repo-wide search, stash-on-switch.

## 1. Git tab layout

`Diff` is renamed `Git` (pinned tab, same slot in the strip). Top to
bottom:

1. **Header:** tab title, branch badge (tappable → branch sheet, §5),
   dirty-file count, refresh.
2. **Message box:** multi-line commit message input (the soft-keyboard
   target) with recent-message reuse (§2.1) and the ✨ Generate button
   (§2.3). Always visible at the top of the content.
3. **Changes sections:** the existing Staged / Unstaged / Untracked
   file lists with per-file and per-hunk staging — unchanged behavior.
   The review surface stays the entry point of the tab. "Explain in
   Codemap" lives beside the list (§4.3, working-tree mode).
4. **Checks row** (§3, *deferred to v2*): run + inline pass/fail chips.
5. **Primary action** (label is contextual on tree + upstream; flow in
   §4.1): "Commit" when the tree is dirty and there is no upstream;
   "Commit & Push" when dirty and upstream exists; **"Push"** when the
   tree is clean and `ahead > 0` — the first-push path, which runs
   `git push -u origin <branch>` to establish tracking; nothing when
   clean and fully in sync. After a successful commit the row updates
   via the existing 5s poll, and a "Create PR" row appears (§4) with the
   ✨ Draft description and Explain buttons once upstream exists and
   ahead === 0.
6. **Ask-AI notes** (existing) at the very bottom.

Routing/testids keep `tab-diff` and the diff URL; the rename is a label
change plus new sections in the existing component tree.

### 1.1 Diff reader behavior

The reader stays unified-only and wrapped (both already correct).
Changes:

- **Collapsed context.** Hunks render changed lines + 3 context lines
  (`git diff -U3`, already produced). Expansion is **per file**, not
  per hunk: a "Context: 3 ▾" control in the file header selects
  3 → 10 → 30 → full. Implementation: re-fetch that file's diff with
  the `context` param (`full` sends `-U100000`; the untracked
  `--no-index` fallback path honors it too) and re-render the hunk
  list. At higher context adjacent hunks may merge — expected; the
  merged hunk still patches cleanly (`git apply` uses context lines
  only to locate the change).
- **Quote carries context.** Expanding a hunk before quoting feeds the
  existing Ask-AI quote with the visible context. No new UI.
- **File-to-file flow + mark-as-reviewed** (*deferred to v2*): the
   "Next file →" link, the "N of M files" progress line, "All reviewed ✓",
   the ✓ toggle, "mark all", and persistent review state are cut from v1.
   The client holds no localStorage and the server tracks no review
   flags (review is not a git-validity question derivable from git
   state). v1 keeps collapsed context + per-file expansion +
   quote-carrying-context + ephemeral collapse-all only.
- **Collapse all** control in the section header.

Not adopted: split/side-by-side view, horizontal scrolling, directory
tree grouping, log/history UI.

## 2. Commit

### 2.1 Server

New endpoints in `internal/httpapi/gitops.go` (read/stage stay in
`gitdiff.go`; same exec-through-Sessions pattern, routes under the
existing `authedProject` group):

- `POST /api/projects/{id}/git/commit`
  Body: `{message: string}` (trimmed non-empty, ≤ 5000 chars). Runs
  `echo <b64> | base64 -d | git -C <dir> commit --no-verify --file -`
  (base64 pattern from `handleGitStageHunk`; stdin handles multi-line
  bodies). `--no-verify` skips repo hooks deliberately: a hook cannot
  be watched or fixed from mobile, and §3 checks are the platform's
  pre-commit answer. Chained with
  `&& git -C <dir> rev-parse --short HEAD` so the response is
  `{ok, commit: <short-sha>, branch}`. Errors: nothing staged (400),
  identity unset (409 with a hint — do NOT set global identity),
  merge-conflict state (409, detected via
  `git -C <dir> diff --name-only --diff-filter=U` non-empty).
- `GET /api/projects/{id}/git/log?limit=10` →
  `{commits: [{sha, subject, relativeTime}]}` via
  `git log --format=%H%x00%s%x00%cr -n 10` (NUL-separated, same parsing
  discipline as `parsePorcelain`). `limit` clamped to 1–50; unborn
  HEAD (no commits yet) returns an empty list, not an error. Powers
  recent-message reuse.
- `GET /api/projects/{id}/git/identity` → `{name, email}` from
  `git config user.name` / `user.email` (per-repo, empty fallback).
  `POST /api/projects/{id}/git/identity` body `{name, email}` (both
  non-empty, ≤ 200 chars, must not start with `-`) runs
  `git -C <dir> config user.name / user.email` — repo-scoped, never
  global. This is the endpoint the 409's inline form posts to.
- `GET /api/projects/{id}/git/status` (existing) — gains
  `upstream: {name, ahead, behind} | null` via
  `git -C <dir> rev-parse --abbrev-ref @{u}` (empty → null) and
  `git -C <dir> rev-list --left-right --count @{u}...HEAD`. Fails
  soft: any error → null upstream, status still 200. Powers the
  "Commit & Push" label and PR-row gating (§4.1).
- `GET /api/projects/{id}/git/diff` (existing) — gains a `context`
  query param (`3` default; `10`, `30`, `full` accepted) passed
  through to `git diff -U<n>`.

### 2.2 Frontend journey

1. Harness finished its run in tmux; user opens the Git tab.
2. Changes list shows Staged / Unstaged / Untracked (no review-progress
   line in v1 — see §1.1).
3. Tap a file → hunks render with 3 context lines → raise context where
   unclear (file-level 10 → 30 → full) → stage per hunk → (✨ Quote
   carries the visible context to Ask-AI).
4. Taps the message box → types (or reuses a recent message from
   `/git/log`, or taps ✨ Generate).
5. Taps **Commit** → success chip with sha → changes refresh via the
   existing 5s poll → clean tree.
6. Identity unset (fetched once per tab open; per-repo, empty
   fallback) → 409 → inline name/email form → posts to
   `POST /git/identity` (repo-scoped, never global) → persists for
   the repo → user taps Commit again to retry.

### 2.3 AI-generated commit message

One structured completion — no tools, no thread (plumbing in §6):

- `POST /api/projects/{id}/git/commit-message`
  Builds the same diff the commit will contain via `CappedDiff`:
  staged diff when the index is non-empty, else unstaged+untracked
  (untracked file contents appended, capped at 50 KB, numstat summary
  line appended when truncated). JSON-schema response `{subject,
  body}`: subject a conventional-commit line (≤ 72 chars, imperative),
  body ≤ 15 lines. AI unconfigured → 409 (same shape as codemap's
  "ai not configured"). Model/parse failure → 502 with detail; the
  message box is left alone.
- Frontend: a "✨ Generate" button beside the message box. Disabled
  when there are no changes (`status.files` empty) or while the commit
  POST is in flight; spinner while generating. Fills subject + body
  into the existing textarea (editable before commit — generation
  never auto-commits). The diff can change under a running harness
  while generating: the snapshot is point-in-time, the user reviews
  the result anyway. No history, no preferences, no per-project
  config.

## 3. Pre-flight checks

> **Deferred to v2.** Pre-flight checks (§3) ship no add/remove/edit
> surface, so the whole feature is cut from v1: no `Checks` config
> field, no `POST /git/checks/run`, no ⚠ confirm gate on Commit. Returns
> once a checks-list UI exists.

Per-project named commands run before commit, results inline. Not git
hooks — results surface in the UI, not in a blocking terminal hook.

### Config

`state.Project` gains `Checks []Check` (persisted in state.json):

```go
type Check struct {
    ID      string `json:"id"`      // short random id
    Name    string `json:"name"`    // "lint", "typecheck", "test"
    Command string `json:"command"` // shell command in the container
}
```

CRUD via `PATCH /api/projects/{id}` (extend the existing body that
carries quickCommands): `checks *[]Check` — nil leaves unchanged,
empty clears, else replaces wholesale with server-generated ids (no
forged collisions). Validate: name non-empty ≤ 40 chars, command
non-empty ≤ 500 chars. No dependency graph.

### Run + results

`POST /api/projects/{id}/git/checks/run` body `{id?}` — with `id`
runs that one check; without, runs every check sequentially in list
order, stopping at the first failure. Each check executes as
`sh -c 'cd <repoDir> && <command>'` (repo-scoped cwd; shellQuote on
the command), via `Sessions.ExecCommand` (synchronous, exit-code
aware), returning `{results: [{id, name, status: "pass"|"fail",
outputTail (≤ 50 lines), durationMs}]}`. Shared 10-min budget across
the run (`context.WithTimeout`); a check killed by the budget reports
`fail` with "timed out".

The frontend fires the `{id}` form one check at a time so chips
progress live and a failure stops the sequence client-side — same
semantics, visible progress. Checks run against the worktree, not the
staged content: a check can pass on files the commit will not include.
Accepted limitation for v1; the ⚠ confirm gate is the mitigation.

### Frontend

Between the changes sections and the primary action: a row per
configured check — run-all button, per-check status chip
(idle/running/✓/✗), tap a chip for the output tail. Commit stays
enabled but shows "⚠ N checks failed — commit anyway?" confirm step.
Zero configured checks render nothing.

## 4. Push / pull / PR

### 4.1 Endpoints

- `POST /api/projects/{id}/git/push` →
  `GIT_TERMINAL_PROMPT=0 git -C <dir> push -u origin <branch>` —
  prompts must fail fast, not hang the exec forever. Detached HEAD →
  400. Output tail returned; rejections (non-fast-forward,
  could-not-read-Username) surface verbatim with the hint "fix
  credentials in the terminal."
- `POST /api/projects/{id}/git/pull` →
  `GIT_TERMINAL_PROMPT=0 git -C <dir> pull --ff-only`; a refused merge
  surfaces verbatim with the hint "fix in the terminal."
- `POST /api/projects/{id}/git/pr` → body `{title, body}` (both
  non-empty, title ≤ 200 chars); runs
  `gh pr create --title … --body … --head <branch>`. Exec is non-TTY
  so gh cannot prompt interactively; missing/`gh`-unauthenticated →
  its error surfaces → 409 with detail, no auto-install, no browser
  auth flow.

Frontend: the primary button is contextual (§1.5): dirty + no upstream
→ "Commit"; dirty + upstream → "Commit & Push"; clean + ahead > 0 →
"Push". Commit-then-push stops at the first failure. **First push on a
branch with no upstream** goes through the "Push" state
(`git push -u origin <branch>` records tracking) — no terminal step
required. **Partial success is surfaced honestly:** commit landed but
push failed → the sha chip shows AND the push error shows, and the
button reads **"Push"** (clean tree, ahead > 0) so retry is genuinely
push-only with no duplicate commit. The "Create PR" row appears once
upstream exists and `ahead === 0` — title prefilled from the commit
subject, body from the message body, result shown as the PR URL
(copyable). States are explicit: pushing… / pushed ✓ / rejected ✗ with
output-tail expandable. The tab description "Commits stay in the
terminal" is replaced.

### 4.2 AI-drafted PR description

One-shot like §2.3 (not a thread), plumbing in §6. The Create PR row
gets a "✨ Draft description" button:

- `POST /api/projects/{id}/git/pr-body` body `{sha}` (the commit just
  made). `sha` must match `^[0-9a-fA-F]{7,40}$` and resolve via
  `git -C <dir> rev-parse --verify <sha>^{commit}` → else 400. Builds
  the commit's diff (`git show <sha>`, capped 50 KB) + commit message
  → JSON-schema response `{title, body}`: title a one-line summary,
  body a PR description with short sections (What/Why/How) ≤ 60
  lines. Same aigen one-shot as §2.3. The result fills the title/body
  fields, editable before Create — never auto-submits.

### 4.3 PR explainer (codemap integration)

A fire-and-forget endpoint kicks off the codemap run from the Git tab;
the user navigates to the Codemap tab and watches the thread:

- `POST /api/projects/{id}/git/explain` → `202 {threadId,
  threadTitle}`; the run continues in the background (execution
  below). Body: `{mode: "working-tree" | "commit", sha?}`;
  `mode=commit` requires `sha` (validated like §4.2) → 400 otherwise;
  `working-tree` with a clean tree → 400. Builds the prompt (thorough
  high-level walkthrough; `working-tree` uses staged-first uncommitted
  changes, `commit` uses the commit's message + diff) with the
  capped-50 KB diff embedded.
- Frontend: an "Explain in Codemap" button on the Create PR row (mode
  `commit`) and beside the changes list (mode `working-tree`,
  disabled when the tree is clean). On 202 the UI shows a chip
  "Explaining… view thread →"; tapping it calls the existing
  `onSelectView('codemap')` — no deep-link param needed, because
  CodemapTab's mount effect already opens `runningThreadId` when set,
  which the busy-slot guarantees. One prompt template, no options, no
  per-project config. AI unconfigured → 409.

Technical execution: `handleCodemap` runs the agent turn on the HTTP
request context (`r.Context()`, 10-min timeout) — a run triggered from
the Git tab dies when the request ends. Fire-and-forget needs three
pieces, all small and additive:

1. **Detached context + goroutine.** New handler does the pre-flight
   `handleCodemap` does — AI config valid, `gitRepoDir`, prompt built,
   `codemapTake(id, "")` to hold the busy slot, `ReserveNewThread`
   (thread + turn 1 placeholder persisted synchronously, exactly as
   normal) — then spawns
   `go func() { defer codemapDone(id); runTurn(...) }()`
   using `context.Background()` (with its own 10-min timeout) instead
   of `r.Context()`. It returns `202 {threadId, threadTitle}`
   immediately.
2. **Response sink.** `executeReservedTurn` writes the HTTP response
   at the end; the background path must not. Extract the turn
   execution tail (run → persist `CompleteTurn`/`FailTurn` + lineage →
   events audit line) into a shared function that takes a
   `writeResponse` func; the sync path passes its current `writeJSON`
   calls, the background path passes a no-op. Everything else — trace
   logging, retryability of a failed placeholder turn, lineage
   persistence — is already request-independent and works as-is.
3. **Surfacing: existing machinery only.** The busy-slot bookkeeping
   already gives the rest for free:
   - `codemapTake/codemapSet(id, threadID)` make
     `GET /codemap/threads` report `runningThreadId` — CodemapTab's
     existing remount-into-run effect opens the running thread and
     shows its spinner on mount. No new polling endpoint, no push
     channel.
   - A second fire while one runs → `codemapTake` fails → 409 (same
     "codemap busy" contract as the sync path).
   - Failed turns stay retryable from the thread (placeholder
     persisted before the model runs — unchanged).
   - Server restart mid-run: the placeholder turn stays
     reserved-never-completed → the thread shows the failed turn,
     retry works. Same semantics as a killed sync run today.

   One gap to close: `ReserveNewThread` alone doesn't set the busy
   slot's thread id for a new chat (the sync path calls `codemapSet`
   after reserve). The background handler must call
   `codemapSet(id, threadID)` before spawning, or `runningThreadId`
   stays "" and the remount-into-run path never opens the thread.

Cancellation: none for v1. A background run cannot be cancelled by the
client (the busy slot blocks a replacement until it finishes or fails;
10-min worst case). Acceptable for v1 — the sync path has the same
property.

Goroutine safety: one detached goroutine per project max (busy-slot
guarantee); it touches only request-independent deps (store, sessions,
obs, events) — nothing pins the dead request. Add an explicit
`recover()` in the goroutine that persists the failed turn if the
handler wrapper doesn't cover detached goroutines.

## 5. Branch switching

### Server

- `GET /api/projects/{id}/git/branches` →
  `{current, detached, local: [{name, relativeTime}], remote: [{name, relativeTime}]}`
  via `git branch --sort=-committerdate
  --format=%(refname:short)%00%(committerdate:relative)` (plus
  `--remotes` for the remote list, `origin/` stripped server-side).
  `current` from `git branch --show-current`; when empty (detached
  HEAD) `current` is the short sha and `detached` is true.
- `POST /api/projects/{id}/git/switch` → body `{branch, create?}`;
  validates the name with `git check-ref-format --branch <name>`
  (covers `-` prefixes, `..`, etc. → 400) and rejects `create` on an
  existing branch (400). Runs `git switch [-c] <branch>`.
  Preconditions: no tracked modifications (porcelain entries whose XY
  has a non-space, non-`?` character → 409 with that file list;
  untracked-only trees switch fine), repo not in merge-conflict state
  (same `--diff-filter=U` check as commit). Detached HEAD reported as
  the sha; switching away is allowed, nothing clever.

### Frontend

The header branch badge is the picker: tap → bottom sheet. Current
branch pinned at top, local list, remote list (remote → the server
strips the `origin/` prefix and creates the tracking branch — standard
`git switch` behavior), create field at the bottom. Sheet shows a
one-line warning when sessions are live: "Running sessions keep their
terminal; the worktree moves." — and, when the current branch has
unpushed commits (`ahead > 0`), an additional "N unpushed commit(s)
stay on this branch until you push." Switching with unpushed-but-committed
work is allowed (git preserves those commits on the source branch); only
staged/working-tree modifications block the switch (§5 preconditions).

## 6. Shared AI plumbing (new package + small refactor)

§2.3, §4.2, and §4.3 use one new `internal/aigen` package on top of
existing pieces; no rewriting of the loop, the lineage, or the thread
machinery:

- **What exists and is reused as-is:** `agent.Config` (the global AI
  credential — validated, normalized), `agent.NewClient`, and the
  retry semantics of `attemptCompletion` (per-call 90 s timeout, 3
  attempts, empty-payload retry). The explainer itself runs the full
  existing codemap pipeline (`codemap.Ask`) unchanged.
- **The one refactor (mechanical):** `attemptCompletion` is unexported
  and takes `(reqMsg, resMsg, where string)` — slog message labels
  specific to the loop. Export it as `agent.Completion` and default or
  struct-ify the label params; call sites in `loop.go`/`format.go`
  update mechanically. Everything else about it stays.
- **What aigen adds (small):** `CompleteJSON(ctx, cfg, system, user,
  schema) (json.RawMessage, error)` — one tools-free completion with
  `response_format: json_schema` (the proven pattern: schema+tools
  breaks providers, schema without tools is what `formatResult` does).
  Built on `agent.Completion` with `maxAttempts=2` (transport retry
  only); the schema call is a single attempt, no raw-text fallback —
  same contract as `formatResult`.
- **CappedDiff stays in httpapi**, not aigen (unexported helper in
  `gitdiff.go`): it runs git via `Sessions`, an httpapi dependency —
  moving it into aigen would need an executor interface for zero
  reuse benefit. Signature
  `cappedDiff(ctx, d, container, dir, includeUntracked bool) (string, bool)`
  — (diff, truncated); staged when any porcelain X is neither space
  nor `?`, else unstaged+untracked contents appended, capped at 50 KB
  with the numstat summary line. Used by §2.3, §4.2, §4.3.
- **aigen takes no dependency on httpapi or codemap** — it sits beside
  `internal/agent` and only imports agent + stdlib, so all three
  features stay thin httpapi handlers (auth, gitRepoDir, build prompt,
  call aigen, persist nothing).

## 7. Quick keys strip (independent feature)

Text entry into TUIs works on mobile (e2e-proven); the gap is keys the
soft keyboard lacks: Esc, Ctrl, Tab, arrows, Ctrl-C/D. A horizontally
scrollable chip row above the terminal input area:

- Sticky Ctrl toggle (next tap sends ctrl+X), Esc, Tab, ↑ ↓ ← →,
  Ctrl-C, Ctrl-D. The strip calls the existing xterm write path with
  synthetic sequences (`\x1b`, `\t`, `\x03`, `\x04`, escape sequences
  for arrows, ctrl-flag form for letters).
- 44px min tap targets (existing `min-h-[44px]` convention), hidden
  when an IME/harness dialog owns the focus or when any non-terminal
  text input (message box, codemap composer, notes) has focus — the
  strip drives PTY input only.

## 8. Out of scope

- Watch mode in all forms (no async run engine, no completion
  notifications). §4.3's fire-and-forget is scoped to the explainer;
  it is not a general background-run engine.
- Merge conflict resolution UI — "do it on desktop." Complex
  behind/ahead-of-base scenarios are also deferred: point the user at
  "Explain in Codemap" (§4.3) for a walkthrough and let them resolve in
  the terminal. The Git tab keeps to the easy paths; no in-app
  conflict/rebase resolution.
- Repo-wide search.
- Stash-on-switch (§5 refuses with the dirty-file list instead).
- Pre-flight checks (§3, config + runner + ⚠ confirm gate) — deferred
  to v2; v1 ships no `Checks` config and Commit is ungated.
- Mark-as-reviewed / file-to-file review state (§1.1) — deferred to v2;
  v1 uses no localStorage and the server tracks no review flags.
- Push credential provisioning (SSH private keys into containers,
  https credential helpers) — push/pull errors surface verbatim with
  a terminal hint instead.
- History/log browsing beyond recent-message reuse.
- Split view, horizontal scrolling, directory tree grouping (§1.1).

## 9. Implementation order

1. Commit endpoint + message box + Git tab rename (§2) — smallest,
   biggest single win, no config surface.
2. Shared aigen package (§6) + the three AI buttons (§2.3, §4.2,
   §4.3) — one capped-diff builder, three thin endpoints.
3. Push + pull + PR, combined Commit & Push (§4.1).
4. Branch switching (§5) — independent, any time after 1.
5. Quick keys strip (§7) — independent of git work entirely.

§4.1's push/pull/PR step depends on nothing from step 2 — the aigen
package is only needed by the AI buttons, so 3–4 can interleave with 2
if the AI work stalls.

## 10. Golden e2e sketch

Extend the existing mobile spec conventions (viewport 390×844,
hasTouch, isMobile; `createProjectViaUI` helper; engineUp guard):

- `git.ship.mobile.spec.ts`: create project → Git tab → make a change
  in the terminal (existing mobile typing test pattern) → changes list
   shows it → expand file → assert hunks render with 3 context lines →
   raise file context → assert U10 render → stage file → tap message box
   → type message → Commit → assert sha chip + clean tree. Push asserted
  against the e2e local git daemon (`allowAnyRepo` test stacks) —
  never github. `gh pr create` / `pr-body` / `commit-message` are
  handler-level tests with a fake `gh` on PATH and the AI config
  pointing at the codemap fake server (the daemon has no gh).
- `git.branch.mobile.spec.ts`: switch branch from the header badge →
  assert badge updates → dirty-tree switch → assert 409 UX.
- Quick keys: tap Ctrl sticky → type `c` → assert the running
  `sleep 300` in the terminal dies (Ctrl-C reached the PTY).

## 11. Existing tests to touch

- `DiffTab` is renamed/extended (component keeps its file or renames to
  `GitTab.tsx` with the testid kept): existing diff-related unit tests
  keep passing (no behavior removed).
- `httpapi_test.go` route registration list grows (commit/log/identity/
   branches/switch/push/pull/pr/explain/pr-body/
  commit-message endpoints).
- (deferred) `state_drift_test.go` coverage for the `Checks` field will
  return with §3.
- `gitdiff_test.go` gains handler-level tests: commit with nothing
  staged (400), commit message shell-escaping (the base64 path),
  switch with dirty tree (409), push/pull output passthrough (incl.
  `GIT_TERMINAL_PROMPT=0` fast-fail), identity POST validation,
  status `upstream` field (present / null / detached), explain
  202 + busy 409.
- `internal/obs/taxonomy.go` grows the new Git event keys (GitCommit,
  GitPush, GitPull, GitPR, GitIdentity, GitLog, GitBranches,
   GitSwitch, GitCommitMsg, GitPRBody, GitExplain) — one
  stable key per use case, per the observability convention.
