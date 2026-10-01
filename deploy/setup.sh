#!/bin/bash
# Pocket Coder one-command setup for a fresh Linux box (EC2 user data or SSH).
#
#   curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
#     --email you@example.com \
#     --smtp-password "xxxx app password"
#
# Five sections, linear and flat: parse_args → validate → install_deps →
# write_env → boot. Unknown flags are a hard error. Credentials go in
# server/.env — the same file local dev uses, and the one both compose
# files read via env_file.
#
# Test hook: --test (or PCODER_SETUP_TEST=1 in the environment, as used
# through `curl ... | VAR=1 bash -s -- ...`) runs a pure-local self-test
# with zero side effects: no root needed, no apt/dnf, no docker, no clone,
# no network, no email. Everything heavy is mocked; write_env still runs
# for real into a temp dir (exercises the Gmail server/.env contract) and
# is deleted on exit. Runtime: ~1s. Full delivery + boot stay manual
# per-release (one real Gmail run).
set -euo pipefail

REPO_URL="${PCODER_SETUP_REPO:-https://github.com/zacharyLYH/pocket-coder}"
REPO_BRANCH="${PCODER_SETUP_BRANCH:-main}"
INSTALL_DIR="/opt/pocket-coder/src"
EMAIL=""
SMTP_PASSWORD=""

log() { printf '[setup] %s\n' "$*"; }
die() { printf '[setup] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: setup.sh --email you@example.com --smtp-password "xxxx app password"

Bootstraps Pocket Coder on a fresh Linux box: installs git + docker,
clones the repo, writes server/.env, validates SMTP delivery, and boots
the production stack on port 8080.
EOF
}

# 1. parse_args — two flags for real runs, plus --test for the self-test.
# (--test also comes from PCODER_SETUP_TEST=1 in the environment, as used
# through `curl ... | VAR=1 bash -s -- ...`: flags become optional test
# defaults so bare `bash setup.sh --test` works.)
parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --email|--smtp-password)
        # A shift past $# neither fails reliably under set -e (it can spin
        # the while loop) nor says anything — guard first, loudly.
        [ $# -ge 2 ] || { usage; die "$1 needs a value"; }
        case "$1" in
          --email) EMAIL="$2" ;;
          --smtp-password) SMTP_PASSWORD="$2" ;;
        esac
        shift 2 ;;
      --test) TEST_MODE=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) usage; die "unknown flag: $1" ;;
    esac
  done
  if [ "$TEST_MODE" = "1" ]; then
    : "${EMAIL:=test@example.com}"
    : "${SMTP_PASSWORD:=test}"
  fi
  [ -n "$EMAIL" ] || { usage; die "--email is required"; }
  [ -n "$SMTP_PASSWORD" ] || { usage; die "--smtp-password is required"; }
  # Same shape the server gates boot on (config.Load emailRe): fail fast
  # here with usage, not halfway through an install.
  [[ "$EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] \
    || { usage; die "--email '$EMAIL' is not a valid email address"; }
}

TEST_MODE=0
if [ "${PCODER_SETUP_TEST:-0}" = "1" ]; then
  TEST_MODE=1
fi

# port_taken reports whether anything LISTENs on a TCP port. Reads
# /proc/net directly — zero dependencies, works on bare images without
# ss/netstat. Falls back to ss/netstat when /proc is unavailable.
port_taken() {
  port_hex="$(printf '%04X' "$1")"
  for f in /proc/net/tcp /proc/net/tcp6; do
    if [ -r "$f" ]; then
      if awk -v p="$port_hex" 'NR>1 && $4=="0A" {split($2,a,":"); if (a[2]==p) found=1} END{exit !found}' "$f"; then
        return 0
      fi
    fi
  done
  if [ ! -r /proc/net/tcp ] && [ ! -r /proc/net/tcp6 ]; then
    if command -v ss >/dev/null 2>&1; then
      ss -ltn 2>/dev/null | grep -q ":$1 " && return 0 || return 1
    elif command -v netstat >/dev/null 2>&1; then
      netstat -ltn 2>/dev/null | grep -q ":$1 " && return 0 || return 1
    fi
    log "no way to check port $1 — continuing"
  fi
  return 1
}

# 2. validate — nothing is installed or downloaded before this passes.
# Ordered cheapest-first. Distro/arch warn, everything else hards.
# (--test never reaches here: test_main exits right after parse_args.)
validate() {
  [ "$(id -u)" = "0" ] || die "must run as root (EC2 user data always is; manual runs need sudo)"
  if [ -f /etc/os-release ]; then
    # shellcheck disable=SC1091
    . /etc/os-release
    case "${ID:-unknown} ${VERSION_ID:-}" in
      "ubuntu 22.04"|"ubuntu 24.04"|"amzn 2023") ;;
      *) log "untested distro (${ID:-unknown} ${VERSION_ID:-unknown}) — will try apt/dnf; report issues if this fails" ;;
    esac
  else
    log "no /etc/os-release — will try apt/dnf; report issues if this fails"
  fi
  arch="$(uname -m)"
  case "$arch" in
    x86_64|aarch64|arm64) ;;
    *) log "untested arch ($arch) — docker itself will fail loudly if truly unsupported" ;;
  esac
  command -v apt-get >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1 \
    || die "no usable package manager (need apt-get, dnf, or yum)"
  if [ -f "$INSTALL_DIR/server/.env" ]; then
    log "existing install at $INSTALL_DIR — re-run: flags updated, dependencies skipped"
    RERUN=1
  else
    RERUN=0
  fi
  if port_taken 8080; then
    if [ "$RERUN" = "1" ]; then
      log "port 8080 taken — assuming our own stack from the previous run"
    else
      die "port 8080 is taken — free it before installing"
    fi
  fi
  disk_kb="$(df --output=avail "${INSTALL_DIR%/*}" 2>/dev/null | tail -1 | tr -d ' ' || true)"
  if [ -n "$disk_kb" ] && [ "$disk_kb" -lt 10485760 ]; then
    die "need at least 10 GB free under ${INSTALL_DIR%/*} (have $((disk_kb / 1024 / 1024)) GB)"
  fi
  if [ -r /proc/meminfo ]; then
    mem_kb="$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)"
    if [ -n "$mem_kb" ] && [ "$mem_kb" -lt 2000000 ]; then
      die "need at least 2 GB RAM (have $((mem_kb / 1024)) MB)"
    fi
  else
    log "cannot read RAM size — continuing, project containers want 2 GB"
  fi
  log "validations passed"
}

# 3. install_deps — missing tools only (a reboot stops the daemon but
# never uninstalls binaries, so the daemon check always runs even when
# the binaries are all present).
install_deps() {
  if [ "${RERUN:-0}" != "1" ]; then
    to_install=""
    for tool in git curl; do
      command -v "$tool" >/dev/null 2>&1 || to_install="$to_install $tool"
    done
    if ! command -v docker >/dev/null 2>&1; then
      if command -v apt-get >/dev/null 2>&1; then
        # docker.io is in universe (the compose plugin is not in
        # Ubuntu's repos, so it comes from GitHub releases below).
        to_install="$to_install docker.io"
      else
        to_install="$to_install docker"
      fi
    fi
    if [ -n "$to_install" ]; then
      if command -v apt-get >/dev/null 2>&1; then
        export DEBIAN_FRONTEND=noninteractive
        apt-get update
        # shellcheck disable=SC2086
        apt-get install -y $to_install
      else
        pm=dnf
        command -v dnf >/dev/null 2>&1 || pm=yum
        # shellcheck disable=SC2086
        $pm install -y $to_install
      fi
    else
      log "git, curl, docker already present — skipping package install"
    fi
    install_compose_plugin
  fi
  ensure_docker_running
  command -v git >/dev/null 2>&1 || die "git install failed"
  command -v curl >/dev/null 2>&1 || die "curl install failed"
  command -v docker >/dev/null 2>&1 || die "docker install failed — see https://docs.docker.com/engine/install/"
  docker compose version >/dev/null 2>&1 || die "docker compose plugin missing after install"
  log "dependencies installed"
}

# ensure_docker_running starts the daemon when the init system did not:
# init scripts first (real boxes), then a direct dockerd launch
# (containers and minimal images have no init). Only launches when the
# daemon is actually unreachable, so a running daemon is never disturbed.
ensure_docker_running() {
  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true
  else
    service docker start 2>/dev/null || true
  fi
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  log "daemon not running under init — starting dockerd directly"
  (dockerd > /tmp/pcoder-dockerd.log 2>&1 &)
  for _ in $(seq 1 12); do
    docker info >/dev/null 2>&1 && return 0
    sleep 5
  done
  tail -20 /tmp/pcoder-dockerd.log 2>/dev/null || true
  die "docker daemon unreachable"
}

# install_compose_plugin fetches the compose plugin from GitHub releases
# when the distro packages did not provide it (Ubuntu) or at all. Pinned
# version for reproducible setups; DOCKER_COMPOSE_VERSION overrides.
install_compose_plugin() {
  docker compose version >/dev/null 2>&1 && return 0
  arch="$(uname -m)"
  case "$arch" in
    x86_64) ;;
    aarch64|arm64) arch="aarch64" ;;
    *) die "unsupported arch for compose plugin download: $arch" ;;
  esac
  ver="${DOCKER_COMPOSE_VERSION:-v2.39.2}"
  dest="/usr/libexec/docker/cli-plugins/docker-compose"
  mkdir -p "$(dirname "$dest")"
  log "installing compose plugin $ver ($arch)"
  curl -fsSL "https://github.com/docker/compose/releases/download/${ver}/docker-compose-linux-${arch}" -o "$dest" \
    || die "compose plugin download failed"
  chmod +x "$dest"
}

# 4. write_env — fresh file, locked down, exactly the Gmail contract.
# SMTP_HOST/SMTP_PORT are honored from the environment when pre-set (lets
# non-Gmail users re-run with overrides); otherwise the server's Gmail
# defaults apply and nothing is written for them.
write_env() {
  mkdir -p "$INSTALL_DIR/server"
  tmp="$(mktemp)"
  {
    printf 'PCODER_LOGIN_EMAIL=%s\n' "$EMAIL"
    printf 'SMTP_USER=%s\n' "$EMAIL"
    printf 'SMTP_FROM=%s\n' "$EMAIL"
    printf 'SMTP_PASSWORD=%s\n' "$SMTP_PASSWORD"
    [ -z "${SMTP_HOST:-}" ] || printf 'SMTP_HOST=%s\n' "$SMTP_HOST"
    [ -z "${SMTP_PORT:-}" ] || printf 'SMTP_PORT=%s\n' "$SMTP_PORT"
  } > "$tmp"
  chmod 600 "$tmp"
  mv "$tmp" "$INSTALL_DIR/server/.env"
  chmod 600 "$INSTALL_DIR/server/.env"
  log "wrote $INSTALL_DIR/server/.env (mode 600)"
}

# test_main — the --test self-test: pure-local, zero side effects, ~1s.
# Runs parse-adjacent checks plus the real write_env into a temp dir
# (exercises the exact Gmail .env contract), asserts the file, then
# deletes everything. Mocks: root, distro, disk/RAM/port, apt/dnf,
# docker, clone, SMTP delivery, boot. Full delivery + boot stay manual
# per-release (one real Gmail run).
test_main() {
  INSTALL_DIR="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$INSTALL_DIR'" EXIT
  write_env
  for line in "PCODER_LOGIN_EMAIL=$EMAIL" "SMTP_USER=$EMAIL" "SMTP_FROM=$EMAIL" "SMTP_PASSWORD=$SMTP_PASSWORD"; do
    grep -qx "$line" "$INSTALL_DIR/server/.env" \
      || die "test: $line missing from .env"
  done
  # Host/port fall through to the server's Gmail defaults — nothing
  # written for them unless explicitly overridden.
  for key in SMTP_HOST SMTP_PORT; do
    val=""; case "$key" in SMTP_HOST) val="${SMTP_HOST:-}";; SMTP_PORT) val="${SMTP_PORT:-}";; esac
    if [ -z "$val" ] && grep -q "^$key=" "$INSTALL_DIR/server/.env"; then
      die "test: $key written without override"
    fi
  done
  # Negative probes run parse_args in a subshell (no recursion into
  # test_main, no root/docker needed): bad invocations must fail LOUDLY
  # with usage on stderr — under curl|bash, silence is all the operator sees.
  probe_reject() {
    desc="$1"; shift
    if out="$( (parse_args "$@") 2>&1 )"; then
      die "test: '$desc' exited 0, want failure"
    else
      case "$out" in
        *Usage*) ;;
        *) die "test: '$desc' printed no usage (output was: $out)" ;;
      esac
    fi
  }
  probe_reject "missing --email value" --email --smtp-password x
  probe_reject "missing --smtp-password value" --email a@b.cd --smtp-password
  probe_reject "bad email" --email not-an-email --smtp-password x
  probe_reject "unknown flag" --bogus
  log "TEST MODE PASS — arg parsing, validation, and .env contract all good (SMTP delivery + boot stay manual per-release)"
  trap - EXIT
  rm -rf "$INSTALL_DIR"
}

# 5. boot — fetch/pull → build → smtp-test → up → wait healthy → banner.
# Update-safe: the same curl command re-runs on an existing install.
# `server/.env` is gitignored so fetch+reset never deletes it (write_env
# refreshes it above), and the pcoder-data volume is never removed here
# — the server seeds a fresh volume from server/.env on first boot and
# leaves existing state alone, so projects and credentials survive.
boot() {
  if [ -d "$INSTALL_DIR/.git" ]; then
    log "repo exists — fetching $REPO_BRANCH"
    git -C "$INSTALL_DIR" fetch --depth 1 origin "$REPO_BRANCH"
    # server/.env is gitignored so reset never deletes it.
    git -C "$INSTALL_DIR" reset --hard "origin/$REPO_BRANCH"
  else
    log "cloning $REPO_URL ($REPO_BRANCH)"
    if ! git clone --depth 1 --branch "$REPO_BRANCH" "$REPO_URL" "$INSTALL_DIR"; then
      die "clone failed"
    fi
  fi
  cd "$INSTALL_DIR"
  # Credentials land in server/.env after the repo exists (fresh clone
  # or fetch+reset), so the file is always there for the smtp-test probe
  # and the compose env_file below.
  write_env
  # The stack network is external (declared, not managed): ensure it
  # before the stack uses it.
  docker network create pcoder-net >/dev/null 2>&1 || true
  log "building images (several minutes on first run)"
  docker compose build
  log "probing SMTP delivery"
  # The image ENTRYPOINT is already ["pcoder"], so the subcommand is
  # appended bare (not `pcoder smtp-test`, which would repeat the binary).
  if ! docker compose run --rm server smtp-test; then
    die "SMTP delivery failed — check the Gmail app-password steps in the README, then re-run this script"
  fi
  log "booting the stack"
  docker compose up -d
  log "waiting for /health (120s)"
  for _ in $(seq 1 60); do
    if curl -fsS http://localhost:8080/health >/dev/null 2>&1; then
      break
    fi
    sleep 2
    if [ "${_}" = "60" ]; then
      docker compose logs --tail=50 server || true
      die "server never became healthy — logs above"
    fi
  done
  ip="$(curl -fsS --max-time 5 http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  [ -n "$ip" ] || ip="<this-host>"
  cat <<EOF

[setup] done — Pocket Coder is live:
[setup]   URL:   http://$ip:8080
[setup]   login: $EMAIL (PIN arrives by email)
[setup]   A setup-complete test email is on its way to your inbox.
EOF
}

parse_args "$@"
if [ "$TEST_MODE" = "1" ]; then
  test_main
  exit 0
fi
validate
install_deps
boot
