# One-command EC2 setup (plan)

Goal: a user pastes **one command** into the EC2 "User data" field and gets a
running Pocket Coder. The only prerequisite is a Gmail app password, passed
as a flag. Everything else — deps, .env, build, boot, verification — the
script does.

## The one command

User data runs as root on first boot, so the script must install its own
dependencies. The one-liner (hosted at a stable raw-GitHub URL):

```sh
curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
  --email you@example.com \
  --smtp-password "xxxx app password"
```

`bash -s --` forwards flags through the pipe. Unknown flags are a hard error
(same philosophy as config.Load's unknown-variable check).

**Remote reachability requirement:** the script must be fetchable from a
fresh EC2 box with no repo present. Hosting options, in order of
preference:
- `raw.githubusercontent.com/OWNER/pocket-coder/main/deploy/setup.sh` —
  works once the file lands on the default branch. Pin to a tag
  (`/v1.0.0/deploy/setup.sh`) once releases exist so setups are
  reproducible.
- A redirect short-link can sit in front later, but the raw URL is the
  source of truth.

This means the script cannot be developed on a branch and used — every
usable iteration requires merging to main first (or testing locally against
a checkout, see the testing section).

## README (rollout step 4, mandatory)

When this ships, the README's top section becomes the user entry point —
above the dev quick start:

1. **Get a Gmail app password** — one link + 3 steps (2FA on, app password
   generate, copy it).
2. **Paste the one command** into EC2 user data (or SSH), with a note on
   which security-group port to open (8080).
3. **Log in** — open `http://<instance-ip>:8080`, enter the email, PIN
   arrives by email.

The dev/local sections (make setup, configuration table)
move below this block. The one command shown in the README must be
byte-identical to the hosted script's actual interface.

## Flags → .env (deliberately tiny)

Duplicated fields are flattened into one flag: for the single-user Gmail
setup, the login email, the SMTP user, and the sender address are the same
address, so one `--email` fills all three. Host and port are omitted — the
server defaults to `smtp.gmail.com:587`, which is the documented setup.

| Flag | .env keys written | Notes |
|---|---|---|
| `--email` | `PCODER_LOGIN_EMAIL`, `SMTP_USER`, `SMTP_FROM` | required, email-regex validated |
| `--smtp-password` | `SMTP_PASSWORD` | required, Gmail app password |

Nothing else is written. `SMTP_HOST`/`SMTP_PORT` fall through to the
server's own defaults; `PCODER_JWT_SECRET` is generated + persisted by the
server into the data dir on first boot. Future non-Gmail providers can add
optional flags without breaking the two-flag contract.

The script writes these into `<install-dir>/server/.env` (chmod 600), which the
production compose file consumes via `env_file`.

## Deliverables

1. **`deploy/setup.sh`** — the bootstrap script (structure below).
2. **`docker-compose.yml`** — production stack (see the Docker
   reconciliation section).
3. **`pcoder smtp-test` subcommand** (small, `cmd/server` or `internal/auth`):
   sends one test email through `auth.SmtpMailer` and exits 0/1. The script
   calls it after building the images — the end-to-end SMTP validation tier.

## Root `.env` — deleted

The root `.env` is gone. Its only job was keeping the JWT signing key
stable across `make dev-seed` wipes; the server now auto-generates and
persists its own signing key (`server/data/jwt-secret`, which dev-seed
keeps), so the file was redundant. Accepted tradeoff: after a dev-seed
wipe, local dev logins reset (sessions live in the wiped state.json).

Cleanup that followed from the deletion (done during rollout):
- Makefile: dropped the root-`.env` include, the root-JWT-secret
  generation blocks in `setup`/`start-docker`, and the JWT export line.
  Dev credentials read from `server/.env` only.
- `docker-compose.dev.yml` env_file points at `server/.env` (required:
  false), matching the Makefile.
- README: remove the root-`.env` mentions.

## Docker reconciliation (dev / prod / e2e)

### Problems today

- `server/Dockerfile.dev` is misnamed: it is the only server image and it is
  already production-grade. The ".dev" suffix suggests a second prod
  Dockerfile should exist — it shouldn't.
- `docker-compose.e2e.yml` duplicates the entire server service block just
  to override env/volumes/ports.
- No production compose exists; the only "full stack" experience runs a
  Vite dev server as the UI.

### Target state: 2 Dockerfiles + 2 full compose files + 1 tiny override

| File | Status | Contents |
|---|---|---|
| `server/Dockerfile` | renamed from `Dockerfile.dev`, content unchanged | The one server image: build on golang, run on alpine |
| `web/Dockerfile` | **new** | Multi-stage: `node:24-alpine` runs `npm ci && npm run build`; `nginx:alpine` serves `dist/` and proxies `/api` + `/ws` to `server:8080` (WebSocket upgrade headers) |
| `docker-compose.yml` | **new** (canonical prod stack) | `server` (no host port) + `web` (nginx, publishes 8080), `env_file: .env` optional, private `pcoder-net`, `restart: unless-stopped` |
| `docker-compose.dev.yml` | trimmed | Dev only, exists for HMR: `server` (publishes 8080) + `web` Vite dev server with the node_modules volume. `env_file: server/.env` |
| `docker-compose.e2e.yml` | shrunk to an override | Only the deltas: inline test env (`PCODER_ALLOW_ANY_REPO=1`, empty SMTP), `${PCODER_E2E_DATA_DIR}` bind mount, `host.docker.internal:host-gateway` extra host, `${PCODER_E2E_API_PORT}` port. Merged at runtime |

The two runtime images (project, preview) are untouched — they are built and
versioned by the server itself, not part of any compose stack.

### How each consumer runs

- **Prod / EC2 setup**: `docker compose up -d` from the clone root (the
  `.env` sits next to `docker-compose.yml`).
- **Dev** (`make start-docker`): unchanged command, now reading
  `server/.env`.
- **E2e** (playwright, `global-teardown.ts`, `e2e-parallel.sh`): merge the
  override and start only the server (playwright runs Vite itself, so the
  nginx web container would be dead weight):
  `docker compose -f docker-compose.yml -f docker-compose.e2e.yml -p <project> up --build server`
  Three call sites update: `web/playwright.config.ts`, `web/e2e/global-teardown.ts`,
  `web/e2e-parallel.sh` (down + up).

Rookie-facing rule of thumb (goes in the README): "one server image, one web
image; `docker-compose.yml` runs the product, `docker-compose.dev.yml` is for
editing the web UI, `docker-compose.e2e.yml` is only ever merged by tests."

## Script structure (`deploy/setup.sh`)

Kept linear and flat: five sections, each a small function, `set -euo
pipefail`, one `log`/`die` pair. ~200 lines target.

```
1. parse_args        # --email, --smtp-password; unknown flag → usage + exit 2
2. validate          # nothing is installed or downloaded before this passes
3. install_deps      # git, docker engine + compose plugin (apt or dnf)
4. write_env         # <install-dir>/server/.env, chmod 600
5. boot              # clone → build → smtp-test → compose up → wait healthy
```

### Section 2 — validations (robust checks, ordered cheapest-first)

- OS: **warn, don't enforce**. If `/etc/os-release` says Ubuntu 22.04/24.04
  or Amazon Linux 2023, proceed silently. Anything else: print a one-line
  "untested distro — will try apt/dnf; report issues if this fails" and
  continue. Hard-die only when no usable package manager (`apt-get`, `dnf`,
  `yum`) exists — that is the real capability boundary, not the distro
  label. (A rookie on Debian or Fedora gets a working setup; a hard
  whitelist just generates support noise.)
- Arch: same policy — warn on anything but x86_64/arm64; Docker itself will
  fail loudly on truly unsupported archs.
- Root (EUID 0) — hard requirement, user data always is; a manual run
  without sudo dies.
- Not already installed: `<install-dir>/server/.env` exists → idempotent re-run
  (update flags only, skip to boot) instead of a destructive second pass.
- Port: 8080 free (`ss -ltn`) — warn if taken (maybe a re-run), die only if
  the re-run detection above didn't already claim it.
- Disk ≥ 10 GB free, RAM ≥ 2 GB — hard, project containers need headroom.
- Flag formats: email regex on `--email`; password non-empty. No network
  calls yet.

### Section 3 — install_deps

- `apt-get`/`dnf` equivalents detected from `$ID`.
- Docker engine via the official convenience path or distro packages;
  compose plugin included. If docker already exists, skip.
- `git`, `curl` (usually preinstalled on EC2 AMIs).

### Section 4 — write_env

- Fresh file, `umask 077`, chmod 600 after write.
- Exactly four keys (`PCODER_LOGIN_EMAIL`, `SMTP_USER`, `SMTP_FROM`,
  `SMTP_PASSWORD`) — no defaults duplicated from config.go.
- Password never echoed, never in `set -x`.

### Section 5 — boot

1. `git clone --depth 1 --branch main` into `/opt/pocket-coder/src`
   (idempotent: fetch + reset if it exists). Opinionated on purpose: no
   `--ref`, no `--install-dir`.
2. `docker compose build`.
3. **SMTP test email** — `docker compose run --rm server smtp-test`
   with the .env loaded. Failure → die with the mailer's error text and a
   pointer to the Gmail app-password guide. Nothing boots on bad
   credentials.
4. `docker compose up -d`; poll `curl localhost:8080/health` until 200
   (timeout 120s); tail server logs on timeout.
5. Print the summary banner: URL (`http://<public-ip>:8080` — resolved from
   EC2 instance metadata, best-effort), login email, and "check your inbox
   for the setup-complete test email".

## Testing the script

Two tiers, cheapest first. KISS: the self-test is pure-local with mocks
(~1s, zero side effects), full delivery + boot stay manual per-release.

`bash deploy/setup.sh --test` (or `PCODER_SETUP_TEST=1` in the
environment, as used through `curl ... | VAR=1 bash -s -- ...`) runs arg
parsing plus the real `write_env` into a temp dir — asserting the exact
Gmail `.env` contract — then deletes everything. Mocked: root, distro,
disk/RAM/port, apt/dnf, docker, clone, SMTP delivery, boot. Flags become
optional test defaults so bare `--test` works.

1. **Static + self-test (CI, every push):** `bash -n` + ShellCheck +
`bash deploy/setup.sh --test`. Catches syntax errors, quoting bugs, and
`.env` contract regressions for free.

2. **Manual Gmail smoke (per release):** one real run with a real app
password — verifies the piece the mocks cannot: Gmail auth quirks and
actual inbox delivery.

3. **Real EC2 gate (per release):** spot instance + the README command in
actual user data, destroy after. The only tier that exercises instance
metadata, security groups, and the true first-boot path.

## Security notes (documented, not solved here)

- User data is readable by root via instance metadata — SMTP secrets in the
  one command are exposed to anyone with root on the box, which is the same
  trust boundary as the .env itself. Acceptable for v1; a prompts-on-SSH
  variant can come later.
- The box serves plain HTTP on 8080. TLS/domain is out of scope for v1 (the
  nginx web container is the natural TLS termination point later).

## Out of scope for this pass

- TLS, custom domains, Let's Encrypt.
- Prebuilt registry images / release tarballs (chosen: git clone + build).
- Non-EC2 provisioning (the script itself is distro-generic, just documented
  for the EC2 user-data flow).

## Rollout order

1. Docker reconciliation: rename `server/Dockerfile.dev` → `server/Dockerfile`,
   add `web/Dockerfile`, add `docker-compose.yml`, shrink e2e to an override,
   update the three playwright call sites. Verify: `make start-docker`, e2e
   suite, manual prod compose up.
2. `pcoder smtp-test` subcommand + test.
3. `deploy/setup.sh` with all validations; verified via the two-tier
   testing ladder (self-test → real Gmail run).
4. Makefile + README cleanup from the root-`.env` deletion; README top
   section rewritten per the README block above (app-password steps → one
   command → login), pointing at the hosted script URL.
