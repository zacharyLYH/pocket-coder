# pocket-coder

Vibe code on the move with your favourite coding CLIs, without sacrificing control and modern ergonomics. Like Devin, T3 Code, and GH Codespaces, but free.

Pocket Coder runs on your own box and gives you a browser terminal, project sessions, git tools, previews, and push notifications on desktop and mobile.

## Run it

On a fresh Linux box, or a Mac with Docker Desktop running. Run this over SSH, or paste it into the EC2 user data field:

```sh
curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
  --email you@example.com \
  --password "your-login-password"
```

On Linux, the script installs Docker. It then clones the repo, writes `server/.env`, hashes the password, and starts the stack on port 8080. On Mac, Docker Desktop must be running first.

1. Open `http://<host>:8080`. On EC2, allow port 8080 in the security group first.
2. Sign in with the email and password you passed to setup. The password needs at least 8 characters. The email is only a login name and never receives mail.
3. Add the SSH key shown in the app to [GitHub SSH keys](https://github.com/settings/keys). Do this on desktop first. Long keys are painful to copy on a phone.
4. On mobile, open the same URL and choose Add to Home Screen. The app installs as a PWA.

Re-run the same command to update. The `pcoder-data` volume survives updates and password changes. Re-run with a new password to sign out every device.

## Developer

### Setup

```sh
make setup                      # tools, deps, demo data
make start-docker               # :8080 + :5173; clears push setup first
make dev-seed                   # reset demo data
make reset-push                 # clear VAPID keys + subscriptions
make nuke                       # remove containers, volumes, local data
```

`make setup` seeds `dev@example.com` / `dev-password-12345`. Use another account with `make setup PCODER_AUTH_EMAIL=you@example.com`. `start-docker` sets the demo password when state has none, so the first login works without seeding.

Dev data lives in `server/data/` (bind-mounted to `/data`). Production uses the `pcoder-data` volume. Treat the volume like the box itself.

Web push keys (`vapid.json`, `push.json`) live in the same directory. `start-docker` clears them every run; `reset-push` does it without starting the stack. An empty store shows Enable again instead of silently resubscribing. See `docs/web-push-spec.md` for details.

### Env

`PCODER_AUTH_EMAIL` is the only value you must set. Production setup writes it to `server/.env` (mode 600). Dev defaults it to `dev@example.com` and needs no file.

Passwords are not env vars. Setup passes the password on stdin and stores only an Argon2id hash.

All other settings are optional and have defaults: `PCODER_DATA_DIR=./data`, `PCODER_BIND=:8080`, the Docker socket, and an auto-generated JWT secret in the data dir. For host runs, put overrides in `server/.env`.

### Tests

```sh
make test                       # setup.sh self-test + Go + web
```

For a faster loop, run `go -C server test ./...`, `cd web && npm run test:unit`, or `cd web && npm run test:e2e:parallel`.
