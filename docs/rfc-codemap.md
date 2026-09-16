# RFC: codemaps and a reusable agent loop

Status: implemented as v1. Covers Devin-style codemaps, the settings to power them, and the agent loop future features reuse.

Styling follows the assistant-ui thread shape (user bubbles right, answers left with avatar, docked round composer, suggestion chips as the empty state), copied as classes with no new dependency. Only shadcn primitives plus two local shadcn-shaped components (avatar, skeleton).

## Summary

A codemap answers "what does this code do" with short sections and clickable snippets. Each snippet names a file and line range. Tapping it opens the file at that line. Read only. Nothing edits code.

Under it sits a small agent loop the whole app can reuse. Codemap is the first caller. Later callers add a prompt and tools without touching the loop.

This is a single user app, so the API key sits plain in state.json. No masking or redaction work ships in v1.

## Decisions already made

- Scope is free prompt plus repo tools, not diff only or repo overview only.
- Settings hold baseURL, API key, and model. Saving runs a live test call first and rejects on failure.
- The file view is a read only overlay. Back returns to the chat with scroll and prompt intact.
- Conversations persist in events.log, not state.json or localStorage alone.
- The loop uses the official openai-go module against any OpenAI compatible baseURL.
- One global key only. When it is missing, the frontend hides the feature and the backend returns 409.

## Config

State gains one block in server/internal/state/state.go.

```
AI *AIConfig json:"ai,omitempty"
AIConfig { BaseURL string, APIKey string, Model string }
```

Env seeds it on first boot only: PCODER_AI_BASE_URL, PCODER_AI_API_KEY, PCODER_AI_MODEL. Add the three names to envKeys in server/internal/config/config.go. Existing values always win.

New file server/internal/httpapi/ai.go.

- GET /api/ai/config returns baseURL, model, and configured bool. It may include the key. Single user box, keep it plain.
- POST /api/ai/test takes baseURL, apiKey, model, or falls back to stored values. It normalizes baseURL first (trim trailing slash, strip a trailing /chat/completions) and then sends one tiny chat completion that declares one dummy tool and requests json_schema output. It returns 200 only when the endpoint accepts both. Otherwise it returns the provider error text, or "endpoint does not support tools" when tools fail but plain chat passes. This catches weak models and thin proxies at Test time instead of at first codemap.
- POST /api/ai/config validates the three fields, runs the same test call, and only writes on success.

New web/src/components/AICard.tsx on the home page next to SshKeysCard. Three inputs, Test button, Save button. Save stays disabled until Test passes. Save failure shows the provider error.

## Agent loop

New package server/internal/agent with three files.

- client.go wraps openai-go with WithBaseURL. It sends chat requests with json_schema response format and tool definitions.
- tools.go holds the registry. A tool is a name, a JSON schema, and a Run function taking ctx, container, and args and returning text. Codemap registers read only tools and nothing else. No write tool ships in v1, and no future writer tool may reuse the codemap prompt or route. v1 ships two tools. Both wrap session.Service.ExecCommand.
  - search_code runs grep -rn, never rg since the sandbox image has no ripgrep. It always passes --exclude-dir=.git --exclude-dir=node_modules plus lockfile and dist excludes, and returns the first 50 hits.
  - read_file runs sed -n for a capped range, max 120 lines and 100 KB. It returns a binary flag when it meets NUL bytes instead of text.
- Every tool arg that reaches bash goes through shellQuote from gitdiff.go. Patterns cap at 200 chars and reject newlines. Paths reuse validGitPath.
- loop.go runs the turn cycle. Send messages, run any tool calls, append results, repeat. Max 8 steps, 60 seconds per model call, 120 seconds total. It stops on client disconnect via ctx cancel. One codemap run per project at a time; a second POST returns 409 codemap busy. The frontend also disables Generate while a run is in flight. Returns the parsed JSON and the trace.

New dir server/internal/codemap holds prompt.go only. It defines the codemap system prompt and the response schema: sections with title, summary, and refs holding path, startLine, endLine, and snippet. Snippet text comes from the model. Path and lines must come from tool output. The prompt says so.

Off intent prompts get a redirect, not an error. The system prompt scopes codemap to questions about this repo's code. When the prompt is not about the repo (general chat, homework, anything with no grounding in tool output), the model returns one section titled "Not a codebase question" with a one line redirect and two or three suggested repo questions. The tab also steers input up front with two chips above the prompt box: "Explain the current diff" and "Map this repo", which prefill the prompt. This is how Devin handles it: fixed entry points plus a scoped prompt, not a hard block.

Routes, next to the git routes in server/internal/httpapi/httpapi.go.

- POST /api/projects/{id}/codemap takes prompt, thread id (auto-created when empty), and optional history. It checks the global key first and returns 409 ai not configured when empty. It mints a turn id, records the repo git sha, and returns 409 codemap busy when a run for that project is already in flight. On success it appends the turn to the thread file and returns turn id, sha, sections, and thread id.
- Thread CRUD under /api/projects/{id}/codemap/threads: list chats newest-first, create, get one with all turns, rename, delete.
- GET /api/projects/{id}/file takes path, start, end, and optional sha. It reuses gitRepoDir and validGitPath from gitdiff.go, reads with sed or cat, caps at 100 KB, and returns path, content, total lines, current sha, and a binary flag. When sha is passed and differs from current, the overlay shows "tree has moved since generated" above the content. Binary and over cap files return the flag with empty content instead of text. Read only. No write path exists.

Each request carries the last few turns as context; the backend holds no session beyond the thread files.

No token streaming in v1. POST stays synchronous with a spinner. The loop writes progress lines with plog (codemap.search, codemap.read) and the tab tails GET logs while the request runs. This reuses the LogsTab pattern and answers "is it stuck" without SSE. Add SSE only when a writer agent needs it.

## Frontend

New web/src/components/terminal/CodemapTab.tsx, added as a fourth tab in TerminalView next to terminal, preview, diff, and logs.

- Prompt box plus Generate button at the top, with "Explain the current diff" and "Map this repo" chips that prefill it. Generate stays disabled while a run is in flight.
- Sections render as accordions with Card, Badge, and Button from the existing set.
- Each ref is a button showing path:Lstart-Lend with a 3 line snippet preview.
- History is a thread picker: the header's history button opens a menu listing every chat newest-first (title, turn count, time, preview). Selecting one loads its turns; New chat starts pending until the first send auto-creates the thread. Each row deletes its own chat.

New web/src/components/terminal/FileOverlay.tsx.

- Fixed full screen layer over the mounted CodemapTab. No route change, so nothing unmounts.
- Header shows back chevron, path, and line range. Body shows pre with line numbers and the target lines highlighted. Binary or over cap files show "not shown" with an "open in terminal" hint instead of content.
- No inputs, no stage buttons. Closing drops no state because it never owned any.

Gating uses the useAiConfig hook on GET /api/ai/config. The Codemap tab does not render at all until configured is true, so key-less backends (including every existing e2e seed) never show it. TerminalView owns the fetch and passes showCodemap into TerminalTabs.

## History

Conversations are one thread per chat, many chats per project. Each thread
is its own file under `$DATA_DIR/codemaps/<escaped-project>/<title>.json`.
Duplicate titles use the familiar ` (1)`, ` (2)` suffixes; the thread ID
inside the JSON remains the stable API identity.
(`server/internal/codemapthreads`): id, title (from the first prompt),
timestamps, and the ordered turn list. The Codemap pane lists threads
newest-first in its history drawer, so past chats are discoverable,
reopenable, and deletable per chat.

`events.log` carries only a lightweight `codemap.turn` audit line per turn
(project, thread id, turn id, prompt excerpt, section/tool counts) — never
the sections themselves.

## Gate

Backend, against a fixture project with a real container.

1. Save config with a bad key fails. Save with a good key passes only after the test call passes.
2. POST codemap with no key returns 409. With a key returns sections where every ref resolves to a real file and line range via GET file.
3. read_file rejects paths outside the repo and ranges over 120 lines.
4. events.log holds the request and response pair after a generation.

Frontend, with mocked API.

1. No key hides the Codemap tab and links to Home AI settings.
2. Generate renders sections and snippet buttons.
3. Tapping a snippet opens the overlay at the right lines. Back returns with scroll and prompt intact.
4. Overlay has no editable controls.

E2E lives in web/e2e/codemap.spec.ts (visual group): home AI card shot, tab hidden without key, prompt with key mocked, mocked one-turn reply plus overlay and back. The project id is fake and session endpoints are route-mocked, so no engine or model is needed.

Backend tests mock the model at the HTTP level (httptest fake on /chat/completions), never at the function level: server/internal/httpapi/codemap_test.go covers config test-before-save, 409/400/404 paths, the full search-read-answer loop, bad model JSON, busy contention, and file validation/binary/moved flags. server/internal/agent/agent_test.go covers baseURL shapes and weak-endpoint mapping. server/internal/codemap/codemap_test.go pins shell quoting and arg rejection.
