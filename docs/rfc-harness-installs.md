# RFC: Harness installs become desired state, and the session dialog stops lying

Status: in progress. Decisions locked with the product owner. Session-name
work overlaps `docs/rfc-session-names.md` and implements its creation and
rename contract for harness sessions; the full name-encoding teardown
described there stays unscheduled.

## Summary

Seven observed bugs and gaps trace back to one root cause: harness installs
are invisible to `state.json`. The registry (what harnesses exist) is
desired state, but which projects have them installed is live-only state,
probed from the container at request time. Everything below — a lying
progress message, uninstallable combos offered as installable, recovery
that forgets installs — follows from that. The fix records installs in
`state.json`, then rebuilds the UI flows on top of it.

## The bugs (each one is a gate)

1. **The new-session picker lists every harness, installed or not.** The
   terminal header's dialog shows all registry entries with a "not
   installed in this project" callout. Selecting one and launching fails
   with `ErrNotInstalled`. The callout admits the list is wrong.
2. **The launch dialog claims it is installing when it is not.** The
   progress text "Installing harness (this may take a minute)…" is static.
   Launches never install (`session.LaunchNamed` refuses: "never a hidden
   download at launch"). What the user watched was a fast CLI-validation
   probe on an already-installed harness. The text is false in both the
   fast case and the failure case.
3. **Users cannot name harness sessions.** The dialog replaces the name
   input with "Sessions run by a harness are named automatically
   (`opencode-1`, …)". The server picks `<harnessID>-<n>`.
4. **Sessions cannot be renamed.** No API, no UI. Users live with
   `opencode-3` forever.
5. **The home page offers to install what is already installed.** With
   harness A installed in project B, the Harnesses card still shows A as
   installable for B. Deriving "already installed" is exactly what the
   recorded state from this RFC makes possible.
6. **Installs are absent from `state.json`.** A user who copies their state
   file to a new machine loses every harness install silently. Nothing
   records the fact that project B wants harness A, so nothing can
   reconcile it.
7. **No end-to-end test covers the flow.** Install → picker → named launch
   → rename has no test asserting the user-visible contract end to end.

## Decisions (locked)

- **Picker shows only installed harnesses.** Install happens on the home
  page; the terminal consumes. The dialog's "install it from the Harnesses
  card first" warning becomes unnecessary because the situation it warns
  about can no longer be selected.
- **Recovery reinstalls eagerly.** When a reconciled container's home
  volume is fresh (state file migrated to a new engine), recorded installs
  are reinstalled during `EnsureContainer`, mirroring the repo re-clone.
  The first attach after migration may take minutes while npm/pip runs.
- **Session names are always required and unique.** Harness sessions take
  a user-supplied unique name at creation, same contract as plain shells.
  This implements the creation and rename contract from
  `rfc-session-names.md`; the `<harnessID>-<n>` encoding itself is not
  torn down in this pass (auto-naming stays as the server fallback when
  no name is supplied, so existing API callers keep working).

## Design

### state.json (bug 6)

`state.Project` gains one field:

```go
Harnesses []string `json:"harnesses,omitempty"`
```

- Written on successful home-page install (`RecordInstall`), idempotent,
  ordered by install.
- Surfaced through the projects index (`Entry.Harnesses`), so the home
  page derives bug 5's "already installed" from desired state, not from
  container probes.
- Recovery reads it: `EnsureContainer` hands recorded harnesses to an
  injected `Installer` (the `SetSSHKeys` pattern — the project package
  must not depend on the session package). `InstallHarness` already
  short-circuits when the binary is present, so reconcile on a healthy
  container is a probe, not a download.
- **Known contradiction to resolve:** `state_drift_test.go` pins "installs
  are live state, the registry entry is desired state" and deletes an
  installed harness mid-run expecting no state mutation. The drift guard's
  assertion must be rewritten for the new contract: a successful install
  now mutates `state.json`, by design. Deleting a harness from the
  registry leaves project records alone (the record names an id that no
  longer exists; reconcile skips unknown ids).

### Session naming (bugs 3, 4)

- `POST /api/projects/{id}/sessions` with `harnessId` accepts `name`.
  Supplied: must pass `session.ValidName`, must not already exist in the
  container (`409` on collision — unique, not ensure-attach). Absent:
  server auto-names `<harnessID>-<n>` as today, so the API stays
  backward-compatible and restart keeps its explicit-name path.
- New endpoint `POST /api/projects/{id}/sessions/{name}/rename` with
  `{"name": "..."}`: validates the new name, rejects collisions, runs
  `tmux rename-session`. Live terminal connections survive (they are bound
  to the tmux session, not the string). Emits `session.rename`.

### Dialog and card (bugs 1, 2, 5)

- `NewSessionDialog`: name input always visible and required; harness
  dropdown lists only `installed` entries; the "not installed" callout and
  the false "Installing harness…" progress text are deleted. Honest
  progress: "Launching <name>…" (installs cannot happen here).
- `HarnessesCard`: the project picker reads recorded installs from the
  projects index. An installed combo shows "Installed" and cannot be
  re-selected; the apply-count label excludes it.

### Gates (bug 7)

One e2e journey asserts the user-visible contract end to end; state.json
content is intentionally not asserted there yet (the drift and recovery
unit/integration tests own the file contract):

1. install a harness into a project from the home card → row reports
   installed; the combo is no longer selectable
2. open the project terminal → new-session dialog lists only installed
   harnesses; name field is required
3. launch with a duplicate name → rejected; launch with a unique name →
   attaches under that exact name
4. rename the session → dropdown and attached session reflect the new
   name; terminal stays connected
5. restart the renamed session → relaunches under the same name

Backend gates:

- unit: `RecordInstall` idempotence; reconcile reinstalls recorded
  harnesses on an empty home volume (mocked installer); unknown recorded
  ids are skipped; launch name required/unique; rename validates and
  rejects collisions
- integration: the recovery smoke test gains a recorded install and
  asserts the binary exists after reconcile on a fresh data dir;
  `state_drift_test.go` rewritten for the new contract

## Blast radius

- Backend: `state.go` (Project field), `project.go` (Entry), `service.go`
  (RecordInstall, Installer, reconcile), `harnesses.go` (record on ok),
  `terminal.go` (launch name, rename handler, route), `session.go`
  (Rename), `main.go` (wire installer).
- Frontend: `types.ts` (Project.harnesses), `HarnessesCard.tsx` +
  `ProjectPicker`, `NewSessionDialog.tsx`, `TerminalHeader.tsx` (rename
  affordance), `TerminalView.tsx` (rename call + session list refresh).
- Tests rewritten, not deleted: `state_drift_test.go` (install mutation),
  harness/session unit tests pinning auto-name-only launches, e2e
  `terminal.stack.spec.ts` (opencode session now needs a name),
  `Dialogs.test.tsx` (dialog contract).
