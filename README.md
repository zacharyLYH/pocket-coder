# pocket-coder

Vibe code on the move with your favourite coding CLIs, without sacrificing control and modern ergonomics. Like Devin and GH Codespaces, but free.

## Run it on a server (EC2)

One command in the EC2 "User data" field (or over SSH as root) and you get
a running Pocket Coder on port 8080. Open that port in the security group.
Re-run the same command anytime to update to the latest version — your data
(volume + `server/.env`) is never touched. Re-run it to rotate the login
password too.

**1. Pick the account email + password** for this instance. One account per
instance; the email is just the login name and never receives mail.

**2. Paste** (replace the email + password):

```sh
curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
  --email you@example.com \
  --password "your-login-password"
```

**3. Log in** — open `http://<instance-ip>:8080` and sign in with that email
and password. The password is hashed (Argon2id) on the box; only the hash is
stored. Rerunning the command with a new password signs out every device.

**4. Connect GitHub (Desktop Recommended)**:
Upon logging in, you'll be prompted to copy the generated SSH key to your [GitHub SSH keys](https://github.com/settings/keys). 
> **Note:** It is highly recommended to perform this initial setup on a desktop device. Copying long SSH keys and navigating GitHub's settings page is significantly easier with a desktop clipboard and screen size. 

Once your key is authenticated, switch to your mobile device and install the app as a PWA (Add to Home Screen) for maximum on-the-go convenience!

## Developer

### Setup
Local dev uses the `docker-compose.dev.yml` stack (Vite HMR), with the
account email in `server/.env`:

```sh
make setup                      # checks tools, creates server/.env, installs deps, seeds demo data
# first run creates server/.env — edit PCODER_AUTH_EMAIL, then re-run make setup
make start-docker               # full dev stack (:8080 + :5173)
make dev-seed                   # re-seed server/data from test/state.mock.json
make nuke                       # teardown containers, volumes, network and server/data
```

The demo login is the seeded email with password `dev-password-12345`
(the hash lives in `test/state.mock.json`). `make start-docker` applies it
automatically on first run when `server/data/state.json` has no password yet,
so you can log in without running `make dev-seed` first.

**Persistent storage**: local dev keeps state in `server/data/` (bind-mounted
at `/data` in the dev container, so `make dev-seed` hits the live file). On
your box it's the `pcoder-data` volume (mounted at `/data` in the container).
The password hash lives in `state.json` there — backups of that volume are
as sensitive as the box itself.

**Env vars** :

| Variable | Description |
|---|---|
| `PCODER_AUTH_EMAIL` | The one login account (email). Required. |
| `PCODER_JWT_SECRET` | Session signing key; auto-generated into the data dir when unset. |
| `PCODER_DATA_DIR` | State dir (default `./data`) |
| `PCODER_BIND` | Listen addr (default `:8080`) |
| `PCODER_DOCKER_SOCK` | Docker endpoint (default `unix:///var/run/docker.sock`) |

Passwords are never env vars: setup pipes the password to the backend's
`set-password` command over stdin, which stores only a salted Argon2id hash.
On a plain host the same command is:

```sh
printf '%s' "$PASSWORD" | go -C server run ./cmd/server set-password
```

### Tests
```sh
make test                       # everything: setup.sh self-test + Go + web
```
