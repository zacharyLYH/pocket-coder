# Butler spec

One butler serves the whole app. It answers questions and does setup work from chat. It runs on Home and in every project with the same history. The current page passes a project id as a hint only.

The butler uses a named AI model from the shared model list. It needs no separate key. See `GET /api/ai/models`.

## Bounds

The butler never reads repo content. It never writes code. It never opens `/workspace/repo`, file content, or diff hunks. It never captures tmux panes or injects into other sessions. Secret values never enter chat. Each request stands alone, with no polling and no background work.

Two tiers exist. Reads run free with no confirm. All writes need an explicit button tap on a confirm card, including devastating ones such as project delete or harness delete. The card states the blast radius. Example: "Delete project api? This removes the container, its 3 sessions, both volumes, and the project record." No typed confirm exists.

If a user asks for code, the butler refuses and points at the Codemap tab. Example: a user asks "read main.go", and the butler answers "I cannot read code. Open the Codemap tab."

The guide also discourages repo work in prose. No repo tool exists, so the wall holds when prose fails. Exec tools stay generic by design, so the guide tells the butler never to run repo-modifying commands on its own initiative.

Out of scope for v1: preview automation and log tail ingestion.

## Tools

Read tools:

* `list_projects` lists ids, branches, and container status.
* `project_detail` returns one project plus live container status and preview slot.
* `list_sessions` returns session names plus alive and harness per session.
* `preview_state` returns preview slots (`stopped`, `degraded`, `ready`) and listening ports.
* `git_meta` returns branch, changed file counts, upstream, ahead or behind state, unborn, and detached. It returns no paths and no hunks.
* `events_tail` reads the event log with a limit and a since filter.
* `health` returns disk free, uptime, docker ping, and server version, plus per-project CPU, memory, and disk numbers when given a project.
* `harness_inventory` lists harnesses plus per-project install state.
* `env_names`, `config_status` return names and booleans only.
* `list_ai_models` returns the configured AI model entries: id, label, model name, and whether a key is stored. Never key values.
* `architecture` returns the architecture notes whole or by section (overview, state, projects, sessions, previews, harnesses, butler). Read it when a question needs design context.

Write tools, each behind a confirm card with a Confirm button:

* `create_project` clones a repo URL and branch, then reports Ready.
* `start`, `stop`, `restart` change one project container.
* `session_create`, `session_kill`, `session_restart`, `session_rename` manage tmux sessions.
* `fanout_exec` runs one command in many projects. Example: a user says "update opencode everywhere", and the butler runs `npm i -g opencode-ai@latest` in each picked project.
* `create_harness`, `install_harness`, `delete_harness` manage harness files.
* `delete_project` removes a container, repo, or metadata by scope.
* `propose_env_fix` names the missing variable. The UI collects the value in a masked field.
* `switch_model` reads one harness config in the home volume and proposes a one-line diff.
* `update_ai_model` renames or relabels one AI model entry by id, behind a confirm card. Label only: the model name, endpoint, and stored key stay untouched.
* `save_shortcut` stores a `state.Shortcut`: either a command shortcut (`Run tests` runs `npm test`) or a key shortcut (`Undo` sends `Ctrl-Z`). Shortcuts store per project.
* `preview_start`, `preview_close` open or close a preview port.
* `git_pull`, `git_push`, `git_switch` run boring git ops.
* Connection fixes stay out of the butler: commit identity is prompted per repo at commit time, and the server deploy key is tested or regenerated from the Git card.

The server enforces the bounds. A tool call with a repo path fails. A test pins this refusal.

## Denylist

The butler has no file-read tool outside its allowlist. The denylist below is explicit so reviews can check it. The inverse holds too: every in-scope domain carries a read tool, and every in-scope verb maps to a tool or a named limit. When no tool matches, the guide says to answer "not available" in one line — never improvise with an adjacent tool.

* User repos: `/workspace/repo`, any project volume, file content, diff hunks, `tmux capture-pane`, `tmux load-buffer`.
* This app's own code: the pocket-coder checkout, `server/`, `web/src`, `state.json` secrets at rest. The butler knows the system from the guide and the tools, never from reading its own source.
* Secret values: API keys, tokens, passwords. Names and labels only.

A request that needs a denied path ends in a refusal plus a redirect. Example: "patch my repo" gets "I cannot touch repos. I can start a session for you, or open the Codemap tab."

## Docs

This file follows the feature template: Goal, Bounds, Tools, UI, Limits, Tests. Each section carries one example with real values. Symbols stay real. Behavior changes update this file in the same PR or the PR stays open.

Limits lists what the feature cannot do yet, so the butler answers "not available" instead of guessing. The guide carries the list. Current entries: no token streaming, one image per turn, no vision fallback, no preview automation, no log tail ingestion, no transcript search or export, no undo for applied writes. When a user hits one, the butler names the limit in one line.

## Phase 1 checkpoints

Ordered. Each lands with tests. No big bang. Phase 2 stays out.

1. Lists for models. `ai` becomes `ai_models`. Old `GET /api/ai/config` goes away for `/api/ai/models` CRUD plus `/api/ai/models/{id}/test`. Keys never render in full. Test runs a live check before save. The git and SSH lists planned here since shipped smaller: commit identity is per repo at commit time (container git config, never state.json), SSH is one server deploy key in the Git card. Example: an empty list disables codemaps until the first model is saved. Unit: lists CRUD plus redaction. Smoke: `go -C server test ./internal/httpapi -run TestAIModels`. E2e: saving the first model enables the codemap tab. The per-feature model picker lands in checkpoint 10, not here.

2. Transcript store. Server stores under `data/butler/<threadID>/` with `manifest.json` plus `N.json` turn files. List sorts by creation time. Delete removes the folder. No cap. No search or export. Example: thread `a1b2` holds `manifest.json` plus `1.json`. Unit: create, get, list order, delete idempotent. Smoke: `go -C server test ./internal/butlerthreads -run TestReserveGetListDelete`.

3. Sheet shell. One `ButlerSheet` serves Home and project. Bot button floats bottom right. Centered floating card on mobile (margins all around), floating chat window on desktop (old-messenger vibe). Blurred backdrop on both, tap outside to exit. Global thread list, project hint chip such as "looking at: api" with clear, empty chips "Wire my key", "Brief me", "New shortcut", composer at bottom. No backend yet, mocked fetch. Example: tap "Brief me" fills the composer. Unit: vitest render with mockFetch. E2e: `web/e2e/butler.spec.ts` checks button opens sheet on Home and project.

4. Turn round trip. `POST /api/butler/turn` returns one 200 with the final answer. SSE streams one line per tool start and finish while the turn runs. Loop caps at 6 steps. No action without a user message. No background work. Example: "brief me" streams "checked 3 projects" then returns a summary. Unit: loop cap plus SSE order. Smoke: POST with fake model returns 200. E2e: pending turn shows status lines under it.

5. Read tools. 12 tools: `list_projects`, `project_detail`, `list_sessions`, `preview_state`, `git_meta`, `events_tail`, `health`, `harness_inventory`, `env_names`, `config_status`, `list_ai_models`, `architecture`. Names and counts only, no paths, no hunks, no secret values. Example: "brief me" chains `list_projects` plus `events_tail` then stops. Unit: each tool against fakes. Smoke: `go -C server test ./internal/httpapi -run TestButlerReadTools`. E2e: mocked read flow renders one summary.

6. Bounds wall. No `/workspace/repo`, no file content, no diff hunks, no `tmux capture-pane`, no `tmux load-buffer`, no app source, no secret values. Repo path in a tool call fails. Code ask refuses and points at Codemap. Example: "read main.go" gets "Sorry, I am firewalled from reading source code by design. As a butler, I help manage the smooth running of all your projects. For source code related queries, ask your own LLM or CodeMaps." Unit: denied path fails. Smoke: test pins the refusal string. E2e: chat shows refusal plus redirect.

7. Confirm card and safe writes. Writes need a Confirm tap, no typed confirm. Card states blast radius. This checkpoint covers `create_project`, `start`, `stop`, `restart`, `session_create`, `session_kill`, `session_restart`, `session_rename`, `preview_start`, `preview_close`, `git_pull`, `git_push`, `git_switch`. Example: "Delete project api? This removes the container, its 3 sessions, both volumes, and the project record." Unit: write without confirm never runs. Smoke: confirm then apply, discard does nothing. E2e: card shows old and new value with Confirm and Discard.

8. Sensitive writes. `delete_project`, `create_harness`, `install_harness`, `delete_harness`, `fanout_exec`, `propose_env_fix` with masked field, `switch_model` with one line diff, `save_shortcut`. Example: "update opencode everywhere" runs `npm i -g opencode-ai@latest` in each picked project only after Confirm. Unit: scope check plus masked value never logs. Smoke: destructive tool needs explicit confirm id. E2e: delete flow shows blast radius before Confirm.

9. Visibility, limits, image, issue. Turn shows collapsed "N steps" row with tool name, args, result summary. Confirm cards stay expanded. Limits answer stays one line, such as "Preview automation is not available." One image per turn, stored beside the thread, deleted with it, vision-less model refuses plainly. Wall case offers a one line issue draft and asks "File this on GitHub?" then one sync create returns the URL. Example: blank preview shot checks `preview_state` before answering. Unit: step row redacts full output, second image rejects, no silent filing. Smoke: image delete cleans folder. E2e: attach button flow plus issue confirm flow.

10. Model picker. The codemap composer and the butler each carry a picker bound to the shared list. No fallback exists. Last picked model wins per feature, or the feature stays disabled when the list is empty. The turn request carries the picked id. Example: codemaps runs gpt-4o for grounding while the butler runs a cheap mini model for briefings. Unit: picker renders from a mocked list. E2e: picking the second model sends its id.

## Phase 2

Usage stats land on the nerdy stuff page, not in the butler sheet. Inference time split by codemaps and butler, time per harness from session age, token counts where the provider reports them, lines changed over time from diff numstat. Reads come from the event log plus harness-native records. No new collection exists. The butler reads the same numbers when asked "how much am I using?"

## Phase 3

Todo lists for longer-horizon tasks live in `lineage.todos` for both Butler and codemaps. The shared `todo` structured tool replaces the full list during the agent loop; later rounds receive its result and can steer by what is done, next, or blocked. Items use `pending`, `in_progress`, `completed`, or `cancelled` status and `high`, `medium`, or `low` priority, with at most one `in_progress` item. The list persists beside the thread and is available to both UIs as a compact checklist. There is no auto-execution and no cross-thread todo state.

## Issues

When the wall looks like a bug or a missing feature, the butler offers to file it. Flow: butler confirms it cannot do the thing, shows a one-line issue draft (title plus two-line body), and asks "File this on GitHub?" On yes, the server runs one sync task that creates the issue with the stored git token and returns the issue URL. No drafts without asking. No silent filing. Duplicates are fine to file; triage happens on GitHub.

## Workflows

Typical workflows ride a second prompt on in-scope turns, after the scope gate: lifecycle, sessions, run-things, keys and models, status briefs, git ops, setup and shortcuts, triage, issues. Each entry holds an intent plus high-level steps, never exact API names — examples Butler may extend, not rules it must follow. Refused turns never pay for them, and follow-ups never replay them. The list grows with the product. Workflow capture from live turns stays out of v1.

## Models, git, and keys

AI models are the one remaining list. Each entry has an id, a label, and its fields, and supports list, add, update, delete, and test. Keys never render in full after save. Each test runs a live check before save, so a stored entry worked once.

`state.json` keeps `ai_models`. The old single-value `GET /api/ai/config` and `POST /api/ai/config` give way to `/api/ai/models` CRUD plus `/api/ai/models/{id}/test`.

Git identity is per repo, not a list: `GET`/`POST /api/projects/{id}/git/identity` reads and writes `git config user.name` and `user.email` inside the project container at commit time — never state.json.

SSH is one server deploy key, not a list: `GET /api/ssh` shows the public half and fingerprint, `POST /api/ssh/test` probes GitHub with it, `POST /api/ssh/regenerate` rotates the pair. Home gates the project list behind a successful test; a failure hands over the key card and a link to https://github.com/settings/keys. There is no per-user key registry.

The codemap composer and the butler each carry a model picker bound to the same list. No fallback exists. When the user picks nothing, each feature uses its last picked model, or stays disabled when the list is empty. Example: codemaps runs gpt-4o for grounding while the butler runs a cheap mini model for briefings.

## UI

One `ButlerSheet` component serves both mounts. A bot button floats at the bottom right on Home and in the project header. It opens a centered floating card on mobile and a small floating chat window on desktop.

The sheet shows one global thread list. A chip names the project hint, such as "looking at: api", with a clear button. The empty state shows preset chips: "Wire my key", "Brief me", "New shortcut". Threads show user bubbles and butler answers. A confirm card shows the old and new value with Confirm and Discard buttons. The composer stays at the bottom.

The sheet also surfaces the three lists like the shortcuts modal does. Models, git identities, SSH keys, and shortcuts each render as rows with edit, test, and delete controls inline. The butler can open the right row on request. Example: a user says "my push fails", and the butler opens the git identity row with the test result beside it.

## Transcripts

The server stores transcripts long term under `data/butler/<threadID>/` with the folder-per-thread shape from codemaps v2. Each folder holds `manifest.json` plus `N.json` turn files. The list sorts by creation time. Users can delete threads. No cap exists. No search or export exists in v1.

## Agency

Responsibility invariant: the butler reads more than it writes. Reads are free and broad, for monitoring the health and running of all projects. Writes manage health, monitoring, and day-to-day operation only — always behind a Confirm card, never for repo content or code.

Each turn opens with one structured scope check (`can_help` under a pinned schema): out-of-scope asks get the pinned refusal with no tool rounds. The gate only ever refutes; anything unparseable falls through to the loop. The butler then runs a bounded tool loop per turn, up to 6 steps. It can chain reads, then propose, then apply after confirm. It takes no action without a user message. It starts no background work.

Example: "brief me" chains `list_projects` and `events_tail`, then writes one summary and stops.

## Tool visibility

Each turn shows a collapsed "N steps" row. It lists the tool name, args, and result summary. Confirm cards stay expanded. Full outputs stay out of chat.

## Streaming

The turn POST returns one 200 with the final answer, as codemaps does. While tools run, the server streams status lines over SSE, one per tool start and finish. The sheet renders them under the pending turn. Example: "checked 3 projects" appears while `events_tail` still runs. Token streaming waits for later.

## Images

Each turn accepts at most one image. The sheet shows an attach button beside the composer. Images store beside the thread under `data/butler/<threadID>/` and delete with it. The server sends the image only to the picked model. A model without vision support refuses the turn with a plain error. Scope is triage of visual issues. Example: a user uploads a shot of a blank preview, and the butler checks `preview_state` before answering.
