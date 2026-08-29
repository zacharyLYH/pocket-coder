# side-project-saviour

Rotate between free coding harnesses while vibe coding on the move.

## Quick start

**Prereqs:** Go, Node, Docker.

```sh
make setup                      # checks tools, creates server/.env, installs deps, seeds from dev/state.mock.json
# first run creates server/.env — edit SPS_LOGIN_EMAIL, then re-run make setup

make start-local                # :8080 backend + :5173 frontend (no docker)
make start-docker               # full stack via docker compose
```

Open `http://localhost:5173` and log in with that email. PIN prints to the server log; set `SMTP_*` in `server/.env` to receive it by email instead (https://help.meetalfred.com/en/articles/8160682-set-up-smtp-for-gmail-app-password-guide).

Manual alternative:
```sh
echo "SPS_LOGIN_EMAIL=you@example.com" > server/.env
make dev-seed                   # same seed step as make setup
```

## Notes

- Containers survive server restarts; next attach recreates them if deleted.
- Stop the server before hand-editing `server/data/state.json`.
- Teardown: `make nuke` (or manually `docker rm -f $(docker ps -aq --filter name=sps-)` + `docker volume rm …` + `docker network rm sps-net` + `rm -rf server/data`)

## Configuration

`server/data/state.json` is the source of truth. Env vars only fill missing values on first boot — to change SMTP, stop the server, delete the `smtp` key from `state.json`, update `server/.env`, and restart.

Config loads from `server/.env` (or `../.env` when run inside `server/`). Real env vars override the file; compose uses `env_file`.

| Variable | Description |
|---|---|
| `SPS_LOGIN_EMAIL` | Login PIN recipient. Required. |
| `SMTP_HOST` | SMTP host (`smtp.gmail.com`). Set `SMTP_*` group for email delivery. |
| `SMTP_PORT` | SMTP port (`587`) |
| `SMTP_USER` | Gmail address |
| `SMTP_PASSWORD` | Gmail app password https://help.meetalfred.com/en/articles/8160682-set-up-smtp-for-gmail-app-password-guide |
| `SMTP_FROM` | Sender address |
| `SPS_DATA_DIR` | State dir (default `./data`) |
| `SPS_BIND` | Listen addr (default `:8080`) |
| `SPS_DOCKER_SOCK` | Docker endpoint (default `unix:///var/run/docker.sock`) |

## Tests

| Command | What it runs | Needs |
|---|---|---|
| `make setup` | First-time setup (tools + `server/.env` + `npm install` + seed `dev/state.mock.json`) | Go, Node, Docker |
| `make start-local` | Backend + frontend directly (no docker) | Go, Node |
| `make start-docker` | Full stack via docker compose | Docker |
| `make test` | Full stack tests (Go unit+integration, web unit+build+e2e) | — |
| `make check-ci` | Run CI workflow locally via act (mirrors GitHub Actions) | Docker, act |
| `make nuke` | Teardown containers, volumes, network and `server/data` | Docker |
