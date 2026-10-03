# Resource lifecycle scripts

Status: draft

The useful abstraction is not wipe tiers. It is:

> Given one Pocket Coder entity, remove or seed every representation of it
> across state, the server data volume, Docker containers, and Docker volumes.

Future scripts can compose these operations like Lego. The same core logic
must serve product behaviour and developer tooling.

## Contract

Every lifecycle operation must be:

- idempotent: running it twice is fine;
- retryable: a partial failure leaves enough information to run it again;
- clean: removing the canonical state also removes its live representations;
- scoped: it only touches resources owned by that entity;
- inspectable: dry-run can show what it found and what it will change.

“Delete the state record” is not success if the matching container, volume,
session, harness install, or chat data is still around.

## Entity operations

Start with vertical operations for these entities:

### Project

Seed/remove together:

- project entry in `state.json`;
- project container;
- repo and home volumes;
- tmux sessions and preview sidecars;
- project codemap threads;
- project harness-install records;
- installed harness files and configs in the container.

Removing a project must remove both volumes. Recreating the container while
keeping its volumes is a separate recovery operation, not a delete.

### Harness

The registry entry and per-project installation are different representations
of one harness relationship:

- global harness registry entry in `state.json`;
- project `harnesses` references;
- installed binary/package in each selected project container;
- native config written at `ConfigPath`.

Harness commands need a target flag, for example:

```text
make harness-seed NAME=opencode PROJECT=owner/repo
make harness-remove NAME=opencode PROJECT=owner/repo
```

Without `PROJECT`, the command may target every project. The command must say
which scope it picked before changing anything.

Removing a harness from the registry must not silently claim that binaries are
gone. It must either clean the selected project containers or report the
containers it could not reach.

### Chats and logs

Seed/remove the matching server-side directories and records:

- project codemap threads;
- global Butler threads;
- observe files;
- audit events.

These do not normally enter project containers, so they should not be mixed
into project cleanup by accident.

### Sessions and previews

These are mostly live state:

- tmux sessions inside the project container;
- preview sidecar containers;
- preview tokens and in-memory workers.

Their cleanup should be part of project removal, and should be safe when the
thing is already gone.

## Edge cases for now

SSH and AI are not ordinary seedable entities.

### SSH

The server key lives in `state.json`, but copies are injected into every
project home volume. It also exists outside Pocket Coder in GitHub or another
forge. A local delete can remove our copies, but cannot revoke the outside
key safely without provider-specific work.

For now, mark SSH cleanup as complete only for Pocket Coder-owned copies and
clearly report the external revocation step.

### AI models

The model registry and API keys live in `state.json`, but the provider account
is external. A seed can restore a test fixture, not a real provider account.
Do not pretend that deleting the local model entry revokes the provider key.

Both need explicit follow-up design before becoming normal seed/remove Lego
pieces.

## Go as the source of truth

Put the lifecycle planner and executor in Go. Product code already owns the
project, harness, state, Docker, session, preview, thread, and SSH services.
The new lifecycle package should call those services instead of duplicating
Docker names and filesystem paths in shell.

Each operation should expose the same shape:

```go
type Operation interface {
	Plan(ctx context.Context, input Input) (Plan, error)
	Apply(ctx context.Context, plan Plan) (Result, error)
}
```

The exact interface can change. The important bit is one planner, one apply
path, and one result shape for:

- the Settings UI;
- Go tests;
- Make targets;
- future operator scripts.

The executor should report found, removed, skipped, and failed resources. A
failed operation returns an error but keeps going where safe, so retrying it
does useful work instead of starting from scratch.

## Make is only the frontend

Make targets should be thin wrappers around the Go command. No business logic
or Docker globbing in the Makefile.

Examples:

```text
make resource-plan ENTITY=project ID=owner/repo
make resource-remove ENTITY=project ID=owner/repo
make resource-seed ENTITY=project ID=owner/repo
make harness-remove NAME=opencode PROJECT=owner/repo
make state-clean                 # compose a clean app state
make state-seed                  # compose a useful dev state
```

Destructive commands require an interactive terminal confirmation. They must
not accept the confirmation phrase from an environment variable or shell
argument.

The same command should work locally and on a deployed box through
`docker compose exec`, as long as it has access to the Docker socket and data
volume. No production checkout needs to be mounted.

## Seed/remove composition

Every safe entity operation gets a complementary seed operation:

```text
seed project       <-> remove project
seed harness       <-> remove harness
seed project chats <-> remove project chats
seed butler chats  <-> remove butler chats
```

Composite developer states are then just command sequences:

```text
make state-seed-full
make state-seed-projects
make state-seed-dockerless
make state-clean-projects
make state-clean-app
```

The composites should be named recipes, not separate implementations. SSH and
AI can participate only after their external-service semantics are defined.

## UI wipe

The current UI action should remain the simpler project-level operation, but
hide it behind a collapsed Danger Zone and require an exact phrase after a
separate `state.json` download.

The UI should not expose the full composition system yet. It calls the same Go
project-remove operation and shows a single “Removing your projects…” state.
When complete, refresh auth/app state and send the user to `/`, the public
`pcoder.bytesbylyh.dev` landing page.

Detailed progress is optional for now. A final success/failure result matters
more. If a later operation deletes the server data volume itself, it must be a
local/external cleanup command, not a request that tries to delete the storage
needed to finish its own response.

## First test slice

Keep the first implementation small:

1. Build one Go project-remove operation.
2. Reuse it from the current `DELETE /api/state` path.
3. Add `make resource-remove ENTITY=project ID=...`.
4. Add Playwright coverage for the hidden UI flow.
5. Add Go tests for idempotency, partial Docker failure, retry, and leftover
   resource detection.
6. Add harness targeting after project removal is solid.
