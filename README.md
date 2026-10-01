# pocket-coder

Vibe code on the move with your favourite coding CLIs, without sacrificing control and modern ergonomics. Like Devin and GH Codespaces, but free.

## Run it on a server (EC2)

One command in the EC2 "User data" field (or over SSH as root) and you get
a running Pocket Coder on port 8080. Open that port in the security group.
Re-run the same command anytime to update to the latest version — your data
(volume + `server/.env`) is never touched.

**1. Get a Gmail app password**:
1. Follow [this guide](https://help.meetalfred.com/en/articles/8160682-set-up-smtp-for-gmail-app-password-guide) to completion and obtain an SMTP password.

**2. Paste** (replace the email + password):
In your box, run:
```sh
curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
  --email you@example.com \
  --smtp-password "xxxx app password"
```

**3. Log in** — open `http://<instance-ip>:8080`, enter the email, the PIN
arrives by email. A setup-complete test email lands in the inbox first,
so you know delivery works before you need it.

## Developer

### Setup
Local dev uses the `docker-compose.dev.yml` stack (Vite HMR) with
credentials in `server/.env`:

```sh
make setup                      # checks tools, creates server/.env, installs deps, seeds demo data
# first run creates server/.env — edit PCODER_LOGIN_EMAIL, then re-run make setup
make start-docker               # full dev stack (:8080 + :5173)
make dev-seed                   # re-seed server/data from test/state.mock.json
make nuke                       # teardown containers, volumes, network and server/data
```

**Persistent storage**: local dev keeps state in `server/data/`. On your box
it's the `pcoder-data` volume (mounted at `/data` in the container).

**Env vars** :

| Variable | Description |
|---|---|
| `PCODER_LOGIN_EMAIL` | Login PIN recipient. Required. |
| `SMTP_HOST` | SMTP host (`smtp.gmail.com`). Set `SMTP_*` group for email delivery. |
| `SMTP_PORT` | SMTP port (`587`) |
| `SMTP_USER` | Gmail address |
| `SMTP_PASSWORD` | Gmail app password https://help.meetalfred.com/en/articles/8160682-set-up-smtp-for-gmail-app-password-guide |
| `SMTP_FROM` | Sender address |
| `PCODER_DATA_DIR` | State dir (default `./data`) |
| `PCODER_BIND` | Listen addr (default `:8080`) |
| `PCODER_DOCKER_SOCK` | Docker endpoint (default `unix:///var/run/docker.sock`) |


### Tests
```sh
make test                       # everything: setup.sh self-test + Go + web
```
