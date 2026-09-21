# Global git setup — RFC (DRAFT)

Status: planning. No code changes yet.

## Problem

Push/pull/clone fail with `fatal: could not read Username … terminal
prompts disabled`. The user is at no fault: the app never asked for git
credentials. The current SSH card holds **public keys only** (deploy-key
direction plus container `authorized_keys`). Nothing in the container
can authenticate *outbound* git, over HTTPS or SSH. Identity is per-repo
container-local, set only after the first failed commit, and dies with
the volume.

## Decision

One global git identity + one HTTPS token (GitHub PAT, `repo` scope),
collected once on Home next to the SSH/AI cards, stored in state.json,
provisioned into every container. No SSH private keys in containers
ever — a token is one string, covers clone/push/pull, and needs no
`known_hosts` custody. The SSH public-key card stays as-is.

## State

```go
// state.state.go — beside AIConfig. GitHub-only v1: repo.go accepts
// github.com URLs, so no host field until GHE is needed.
type GitConfig struct {
    Name  string `json:"name"`
    Email string `json:"email"`
    Token string `json:"token"` // GitHub PAT, repo scope; never returned to the FE
}
```

`Document` gains `Git *GitConfig`. `configured = Name+Email+Token
present`. No migration: absent = unconfigured, and the gate below
applies to existing installs too. state.json is already 0600 +
atomic-rewrite; the token is never logged (same rule as the AI key).

## Endpoints (AI-config pattern: test-then-save)

- `GET /api/git/config` → `{name, email, hasToken, configured}`
  (shape mirrors `GET /api/ai/config`, which likewise omits the secret).
- `POST /api/git/test` `{name, email, token}` → 200, or 502 with
  detail. Format-checks locally, then live-checks the token
  server-side (`GET https://api.github.com/user`, `Authorization:
  Bearer <token>`; 200 = good, 401/403 = bad). No repo needed, so it
  works before any project exists. Nothing is saved.
- `POST /api/git/config` → test passes, then `Mutate` save (exactly
  `handleSaveAIConfig`'s flow).

## Gating (how "block until git works" works)

- **Save-time proof** (above) is the primary check: you cannot save a
  token GitHub rejects.
- **Creation gate**: `POST /api/projects` returns 409 `git not
  configured` while `!configured`; `ProjectsCard` disables create with
  an inline "Set up Git below" hint. Creation is the only entry point
  that needs git, so terminal, codemap, and existing projects keep
  working.
- **Rot detection** (tokens die): push/pull/clone error-mapping learns
  one auth shape — `401/403`, `could not read Username`,
  `Authentication failed` → 502 `Git credentials rejected — update
  them in Git setup` (distinct from conflict output, which keeps the
  terminal hint). No background re-verification scheduler.

Rejected alternative: blocking the whole app. Existing installs with
running projects would lock out on upgrade; creation-gating gets the
same guarantee at the only place it matters.

## Provisioning (beside injectSSHKeys)

`project.Service.injectGitConfig`, called from `Create` and the
`EnsureContainer` recreate path, warn-only like SSH so it never bricks
boot:

- `WriteFile /root/.git-credentials`
  (`https://x-access-token:<token>@github.com`, container chmod 600),
- `git config --global credential.helper 'store --file
  /root/.git-credentials'`,
- `git config --global user.name/email`.

Rotation: `EnsureContainer` early-returns while the container lives, so
updates would not propagate. Every git API call goes through
`gitRepoDir`, so it gets one idempotent `EnsureGitConfig`: compare a
marker file
(`/root/.git-configured-sha`, hash of name+email+token) and re-inject
on mismatch — one extra `cat` per call, zero on steady state.
Per-repo `POST /git/identity` stays as a container-local override; the
commit 409 identity flow stays as the fallback.

## Frontend

- New `GitCard` on Home beside SSH/AI: three fields, Test then Save,
  prefilled when configured. Edit-in-place like the other cards, so
  there is no separate settings page to build or find.
- `ProjectsCard`: disabled create + hint while unconfigured.
- Git-tab copy stays as-is; once this ships, the push/pull error hint
  text changes from "fix in the terminal" (auth case) to "update them
  in Git setup". Per-repo identity form stays.

## Out of scope

SSH private keys or agents in containers, OAuth/device flow, per-repo
credentials, GitHub Enterprise (repo.go is github.com-only today),
scope advice beyond `repo`, token-expiry reminders.

## Tests

- `state_drift_test.go`: Git field round-trips.
- Handler tests with mocked docker + fake `api.<host>/user`: bad token
  → 502 and nothing saved; good token → saved; `GET` never leaks the
  token; create-project 409s while unconfigured.
- Provisioning test: `WriteFile` contents + global config commands;
  marker mismatch re-injects, match skips.
- FE: route-mocked card test (test-then-save, edit flow) + creation
  gate; push-failure e2e asserts the new "update in Git setup" copy.
