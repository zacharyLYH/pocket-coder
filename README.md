# side-project-saviour
Vibe coding is expensive and developers are busy people! What if you could vibe code use and rotate between free coding harnesses while on the move?

## Run locally

You need Go, Node, and a running Docker engine. `SPS_LOGIN_EMAIL` is the only required setting; everything else has a working default.

**1. Set the login email** in `server/.env` (gitignored):

```
SPS_LOGIN_EMAIL=you@example.com
```

**2. Seed dev state** (optional). This resets `server/data/` from `server/dev/state.mock.json`, which holds a real GitHub repo and a full harness registry:

```
make dev-seed
```

Skip this to start empty instead.

**3. Start the stack:**

```
go -C server run ./cmd/server        # backend on :8080
npm -C web run dev                   # frontend on :5173 (proxies /api + /ws)
```

**4. Log in** at `http://localhost:5173` with `SPS_LOGIN_EMAIL`. Without SMTP configured, the PIN prints to the server log. To receive the PIN by email instead, add the `SMTP_*` variables to `server/.env` before the first boot (see Configuration).

Project containers survive server exits by design (tmux sessions keep running). If you delete a container by hand, the next terminal attach recreates it. To edit `server/data/state.json` by hand, stop the server first: the server reads the file at boot and rewrites it on every change.

When you're done hacking, tear down everything the server created:

```
docker rm -f $(docker ps -aq --filter name=sps-)
docker volume rm $(docker volume ls -q --filter name=sps-)
docker network rm sps-net
rm -rf server/data                   # optional: reset all local state
```

## Configuration

`server/data/state.json` is the single source of truth. On boot, environment variables fill in only what the file is missing. After that, editing `.env` has no effect on a value the file already has. To change SMTP settings, stop the server, delete the `smtp` section from `state.json`, update `server/.env`, and start the server again.

The server loads `server/.env` from its working directory when you run it on the host — so `server/.env` when you `go run` inside `server/`, or the repo root. In `docker compose`, pass them via `env_file` instead. Real environment variables always win over the file.

| Variable | Description |
|---|---|
| `SPS_LOGIN_EMAIL` | Email address that receives login PINs. Required. |
| `SMTP_HOST` | SMTP server (Google: `smtp.gmail.com`). Optional; set the `SMTP_*` group to get PINs by email. |
| `SMTP_PORT` | SMTP port (Google: `587`) |
| `SMTP_USER` | Gmail address |
| `SMTP_PASSWORD` | Gmail app password |
| `SMTP_FROM` | Sender address |
| `SPS_DATA_DIR` | State directory (default `./data`) |
| `SPS_BIND` | Listen address (default `:8080`) |
| `SPS_DOCKER_SOCK` | Docker engine endpoint (default `unix:///var/run/docker.sock`) |

## Tests

| Command | What it runs | Needs |
|---|---|---|
| `make test` | Go unit tests | — |
| `go -C server test -tags=integration -count=1 ./internal/...` | Go tests against a live Docker engine, including the `state.json` recovery smoke test | Docker |
| `npm -C web run test:unit` | Frontend unit tests | — |
| `npm -C web run test:e2e` | Browser end-to-end tests against the real backend | Docker, Go |
| `make check` | Lint, all Go tests, frontend unit tests, build, e2e | Docker, Go |
