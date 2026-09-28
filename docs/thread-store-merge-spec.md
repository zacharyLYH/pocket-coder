# Thread Store Merge Spec

Status: proposed. One persistence layer for all agent chats. Review before
implementation.

## What exists today

Two packages persist agent chats with the same design, implemented twice:

- `butlerthreads` (535 lines) — `$DATA_DIR/butler/<threadID>/`
- `codemapthreads` (589 lines) — `$DATA_DIR/codemaps/<escaped-project>/<threadID>/`

Both: folder name = MintID hex thread id; `manifest.json` = {id, title,
createdAt} only; `N.json` = one user-side turn; `N.lineage.json` = debug
lineage, written once at Complete/Fail, never read for context; atomic-ish
writes; one store mutex; `ReserveNewThread / ReserveFollowup /
CompleteTurn / Get / List / Delete` with the same semantics; `TitleFromPrompt`
and `Summary` with the same shapes.

The diffs, in full:

| | butler | codemap |
|---|---|---|
| Turn payload | `Answer string` + `Steps []Step` + `ProjectHint` | `Sections/Tools json.RawMessage` + `SHA` |
| Addressing | global (`Get(id)`) | project-scoped (`Get(project, id)`) |
| Extra store methods | Approvals sidecar (`approvals.json`), `ErrPendingApproval` reserve guard | `FailTurn` (error-filling persist), `BeginRetry` (retry failed turn), `DeleteProjectDir` (project-delete cascade) |
| Write discipline | tmp+rename | direct write (torn reads accepted) |
| validID | exactly 24 hex | ≤128 hex |
| Legacy sweep | none | sweeps pre-folder flat files |

Everything else — manifest read/write, turn-N file naming rules, sorted
reassembly, List summaries, Delete idempotency — is the same logic with
different names.

## The merged design

One package: `internal/threads`. One store type. The turn body becomes an
opaque payload the caller owns.

```go
package threads

// Turn is the envelope both products share. Payload is the caller's
// own shape, opaque to the store.
type Turn struct {
    TurnID      string          `json:"turnId"`
    Prompt      string          `json:"prompt"`
    Time        time.Time       `json:"time"`
    Error       *string         `json:"error"`
    SHA         string          `json:"sha,omitempty"`
    ProjectHint string          `json:"projectHint,omitempty"`
    Payload     json.RawMessage `json:"payload,omitempty"`
}

type Store struct{ /* ... */ }

// Scope: the one structural difference. A scope key prefixes the folder
// path; an empty scope (butler) resolves to the root. One code path.
//
//   butler:  $DATA_DIR/butler/<id>/              scope = ""
//   codemap: $DATA_DIR/codemaps/<proj>/<id>/     scope = project

func (s *Store) ReserveNewThread(scope, prompt string, payload json.RawMessage) (threadID, turnID string, err error)
func (s *Store) ReserveFollowup(scope, threadID, prompt string) (n int, turnID string, err error)
func (s *Store) CompleteTurn(scope, threadID string, n int, turn Turn, lineage []byte) error
func (s *Store) Get(scope, threadID string) (Thread, error)
func (s *Store) List(scope string) ([]Summary, error)
func (s *Store) Delete(scope, threadID string) error
func (s *Store) ReadTurnLineage(scope, threadID string, n int) ([]byte, error)
```

The optional behaviors stay in the same package as small sidecars, each a
no-op for the caller that doesn't use it:

- **Approvals** (butler): `approvals.json` sidecar + the `ErrPendingApproval`
  reserve guard becomes `Store.Approvals bool` — a store-level toggle. When
  on, reserve checks and the five approval methods exist; when off, they are
  never called.
- **Retry** (codemap): `BeginRetry` moves over as-is; it is turn-envelope
  logic (rewrite highest N), not codemap logic.
- **FailTurn** (codemap): collapses into `CompleteTurn` — both persist the
  envelope + lineage; "fail" is just a turn with `Error` set. Butler's
  handler already does exactly this inline (`turn.Error = &msg` then
  Complete). One method fewer.
- **DeleteProjectDir** (codemap): `DeleteScope(scope)` — the scope-prefix
  rule applied at the top. Used by project delete to cascade codemap
  threads; butler never calls it.

### Retry is shared machinery (both callers)

`BeginRetry` (rewrite the highest-N failed turn in place) is turn-envelope
logic and serves both products:

- **Codemap** already exposes it (pinned SHA rerun, FE retry button).
- **Butler gains it** as a follow-on feature: a `/butler/threads/{tid}/retry`
  endpoint + FE button on the last failed turn. It is safe precisely because
  butler writes are propose-only — a replayed stale intent can only produce
  a confirm card, never a mutation — and the existing
  `ErrPendingApproval` guard blocks retry while a card is undecided. Retry
  gives butler an explicit failed→rerun affordance instead of "red text,
  retype and hope".

### DeleteProjectDir is load-bearing

Called from project delete (`project/service.go`): deleting a project with
scope=metadata/all must cascade its codemap threads, which are keyed by
project id with no other GC. It stays, generalized as `DeleteScope`.

## On-disk compatibility

None — existing thread data is disposable dev data and gets wiped:

- **Directory layouts are unchanged**: `butler/<id>/` and
  `codemaps/<proj>/<id>/` keep their exact shapes, produced by the same
  scope-prefixing rule (`scope + "/" + id`, empty scope = no prefix).
- **N.json changes shape** in both products. Old files simply stop being
  read: unparseable turns are skipped by Get/List (the same behavior as a
  half-written file today), so old threads vanish rather than half-appear.
  No read-shim, no migration, no legacy decoders.
- `server/data/butler/` and `server/data/codemaps/` are deleted as part of
  the migration — a fresh start rather than a slow fade.

## Callers after

- `butler.go` / `butler_writes.go` / `butler_confirms.go`: swap
  `butlerthreads.X` → `threads.X` with scope `""`, plus typed
  marshal/unmarshal helpers for `ButlerPayload{Answer, Steps}` (~15 lines).
- `codemap.go` / `codemap_explain.go`: swap `codemapthreads.X` → `threads.X`
  with scope = project id, `Payload` = marshaled `CodemapPayload{Sections,
  Tools}`.
- `httpapi.Deps` carries one store type twice (two instances, different
  roots) instead of two types.
- `main.go`: two `threads.New` calls, roots unchanged.

## What this buys

- **−400 to −450 net lines** (two 500+-line packages → one ~600-line package;
  tests merge the same way).
- The mental model becomes literal: *one thread store, one folder layout,
  one envelope* — a third agent feature gets persistence for free.
- Reserve/complete/list semantics get one test suite instead of two
  drift-prone copies.

## Not in scope

- Handler/endpoint consolidation (SSE vs batch, approvals flow vs sections
  response stay per-product).
- The busy/CAS slots stay where they are (~20 lines each, product-scoped
  semantics).
- The butler retry endpoint + FE button land as a small follow-on after the
  store merge (store ships `BeginRetry` shared; the endpoint is ~30 lines of
  handler mirroring `handleCodemapRetry`).
- Any change to what the FE sees: JSON response shapes are preserved by the
  typed payload wrappers.

## Risks

- **Old threads disappear** — accepted explicitly (data wipe, no shim).
- **validID divergence** (24 vs ≤128 hex) — unified to ≤128 hex; strictly
  more permissive.
- **Write discipline divergence** (tmp+rename vs direct) — unified to
  tmp+rename; strictly safer.

## Sequencing

1. Land `internal/threads` with the merged store + full test suite
   (port both existing test files, merge overlapping cases).
2. Switch butler onto it. Green build, endpoints unchanged.
3. Switch codemap onto it (includes the FailTurn→Complete collapse).
4. Delete `butlerthreads/` and `codemapthreads/`. Each step is green
   independently; step 4 is pure deletion.
