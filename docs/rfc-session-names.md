# RFC: Session names become user-owned identifiers

Status: proposed, not yet scheduled. Greenfield product; no live sessions
exist, so no migration path is included by design.

## Summary

Today a tmux session's name does two jobs at once. It is the address the rest
of the system uses to reach the session, and it secretly encodes what the
session is: a name matching `<harnessID>-<n>` means "this runs the harness
with that id", anything else means plain shell. This RFC proposes splitting
those jobs. Session names become ordinary, user-chosen, unique identifiers.
What a session runs becomes attached data that travels with the session. Users
can also rename sessions after creation.

## Why change it

The current encoding defends itself with machinery that exists for no other
purpose:

- **The reservation system.** A user cannot name a shell session "opencode" or
  "opencode-1", because those names would impersonate harness sessions in the
  picker and confuse restart logic. A guard test pins this refusal.
- **Restart guesses from characters.** Restart parses the base of the name and
  looks up a harness. This has a latent wrong-result bug: create a shell named
  `dev-2` before registering a `Dev` harness, restart later, and the platform
  relaunches a bash session as an agent.
- **Renaming is impossible**, so users live with labels like `opencode-3`.
- **Every future feature inherits the tax.** The Phase 10 reviewer derives its
  targetable-session list from the same convention; worktree-based multi-agent
  support would inherit it too.

Under the proposed model none of this machinery needs to exist. A name is just
a unique key; what the session runs is data read alongside it.

## Proposed behavior

- **Creation always requires a user-supplied name.** No server-side default
  suggestions. The name must be unique among the project's live sessions;
  duplicates are rejected with a clear message. Character rules stay as today
  (safe as exec args, URL segments, and event payloads).
- **Harness association lives in `state.json`.** Each project carries a
  session registry mapping session name to the harness id that launched it,
  written at launch, updated on rename, removed on kill. Shape:
  `"sessions": { "<projectID>": { "<sessionName>": "<harnessID>" } }`,
  omitted entirely when empty (`omitempty`), so fresh installs and projects
  without agents carry zero new bytes. Plain-shell sessions never appear in
  the registry: absence of an entry means shell.
- **Sessions can be renamed.** Renaming updates the identifier everywhere it
  is used going forward, including the registry key. Live terminal connections
  must not break; they are already bound to the running process, not the
  string.
- **Restart keeps working.** Killing and relaunching a session preserves both
  its identity and its harness binding.
- **Lifecycle rules that protect existing guarantees.** Registry writes are
  ordinary `Mutate` calls. Pruning of stale entries happens only when the
  running system observes, via tmux, that a registered session no longer
  exists; boot-time recovery itself must not rewrite the file. This keeps the
  established invariant that recovery leaves `state.json` byte-identical (see
  `recover_integration_test.go`). Greenfield product: no migration path for
  pre-existing sessions exists or is needed.
- **Existing behavior otherwise unchanged.** Launch flow, validation, kill,
  attach, resize, event payloads, and the reviewer's send flow behave as
  today; only name semantics and the picker's source of truth change.

## Blast radius (what will need touching)

Inventory, not instructions:

- Backend: `state.Document` (the new optional registry), `session.ParseBase`,
  `session.HarnessSuffixed`, `nextName` numbering, the reservation check in
  `handleCreateSession`, restart classification in `handleRestartSession`,
  `harnessNameConflict`, and the `Entry` shape returned by `List`.
- Frontend: `NewSessionDialog` (name field becomes required in all cases),
  `TerminalView`/`TerminalHeader` (picker plus rename affordance), and the
  Phase 10 reviewer's targetable-session derivation once built.
- Unit tests known to pin current behavior:
  `TestCreateSessionRejectsHarnessNamespaceNames`,
  `TestLaunchNumberingAndRepoDir`, `TestLaunchNamedPinsRestartArgv`,
  `TestValidName`, parts of `TestCreateSessionLifecycle` and
  `TestRestartBareHarnessNameRestartsPlainShell`. These get rewritten to pin
  the new contract rather than deleted wholesale.
- E2E: `terminal.session.spec.ts` (multi-session dropdown, new-session dialog)
  and visual snapshots showing generated names in the dropdown.

## Effect on the persisted files

- `state.json`: gains the optional `sessions` registry described above. The
  `Document` struct grows one field; nothing existing moves. Fresh documents
  contain no `sessions` key at all.
- `server/dev/state.mock.json`: intentionally unchanged, and it must stay
  free of session entries even after implementation. Sessions are runtime
  objects, not desired state to seed; a seeded registry entry whose tmux
  session does not exist would either linger forever or force boot-time
  pruning, and pruning at boot is exactly what the lifecycle rules above
  forbid so that recovery stays byte-identical. The mock keeps describing
  exactly what it means to describe: identity, projects, harnesses.

## Gate

1. Create a shell session named `dev`; create another named `opencode`; both
   succeed and neither is registered as an agent.
2. Refuse a duplicate name within the same project while both sessions are
   alive; allow the same name again after one dies.
3. Launch through a harness; confirm the registry records
   `{project: {name: harness}}`; kill the session and confirm the entry is
   removed from `state.json` on disk.
4. Rename a live session with an open terminal: the connection survives, the
   header and picker show the new name immediately, the registry key follows
   the rename, and attaching via the new name works.
5. Restart a renamed, harness-bound session under its same name: it comes back
   as the same kind of session with its binding intact.
6. Boot against a seeded `state.json` with no sessions key (the committed
   mock): recovery completes without rewriting the file.
7. Full existing suite green except the deliberately rewritten tests listed
   above; e2e and visual snapshots updated to match the new dialog and picker.
