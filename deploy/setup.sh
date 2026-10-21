#!/bin/bash
# Pocket Coder one-command setup for a fresh Linux box (EC2 user data or SSH)
# or a Mac with Docker Desktop.
#
#   curl -fsSL https://raw.githubusercontent.com/zacharyLYH/pocket-coder/main/deploy/setup.sh | bash -s -- \
#     --email you@example.com \
#     --password "your-login-password"
#
# Four sections, linear and flat: parse_args → validate → install_deps →
# boot. Unknown flags are a hard error. Credentials go straight to the
# backend's set-password command (email as a flag, password on stdin): the
# email and the Argon2id hash land in state.json — the app's only identity
# source — and no .env is written.
#
# Test hook: --test (or PCODER_SETUP_TEST=1 in the environment, as used
# through `curl ... | VAR=1 bash -s -- ...`) runs a pure-local self-test
# with zero side effects: no root needed, no apt/dnf, no docker, no clone,
# no network. Stub binaries on PATH stand in for the platform tools, so
# validate, install_deps, and even boot() execute for real against a call
# log. Runtime: ~2s. Live boot stays manual per-release.
set -euo pipefail

REPO_URL="${PCODER_SETUP_REPO:-https://github.com/zacharyLYH/pocket-coder}"
REPO_BRANCH="${PCODER_SETUP_BRANCH:-main}"
# Default home for the checkout. resolve_install_dir (in validate) may move
# it under $HOME when /opt is not writable — explicit PCODER_INSTALL_DIR
# always wins, and an existing install keeps its location.
INSTALL_DIR="${PCODER_INSTALL_DIR:-/opt/pocket-coder/src}"
EMAIL=""
OS="$(uname -s 2>/dev/null || printf unknown)" # Darwin vs Linux — Mac has no apt/dnf/systemd
# Homebrew + Docker Desktop helpers live outside the default PATH in
# non-interactive shells (curl|bash): pick them up so git/docker and the
# desktop credential helper resolve on a stock Mac.
for brewdir in /opt/homebrew/bin /usr/local/bin "$HOME/Applications/Docker.app/Contents/Resources/bin" /Applications/Docker.app/Contents/Resources/bin; do
  case ":$PATH:" in *":$brewdir:"*) ;; *) [ -d "$brewdir" ] && PATH="$brewdir:$PATH" ;; esac
done

log() { printf '[setup] %s\n' "$*"; }
die() { printf '[setup] ERROR: %s\n' "$*" >&2; exit 1; }

usage() {
  cat >&2 <<'EOF'
Usage: setup.sh --email you@example.com --password "your-login-password"

Bootstraps Pocket Coder on a fresh Linux box or a Mac (Docker Desktop):
installs git + docker, clones the repo, stores the login account in
state.json, and boots the production stack on port 8080.

Re-run the same command anytime to update — and to rotate the password.

Mac: install Docker Desktop first
(https://www.docker.com/products/docker-desktop/), launch it, then re-run.
EOF
}

# 1. parse_args — two flags for real runs, plus --test for the self-test.
# (--test also comes from PCODER_SETUP_TEST=1 in the environment, as used
# through `curl ... | VAR=1 bash -s -- ...`: flags become optional test
# defaults so bare `bash setup.sh --test` works.)
parse_args() {
  while [ $# -gt 0 ]; do
    case "$1" in
      --email|--password)
        # A shift past $# neither fails reliably under set -e (it can spin
        # the while loop) nor says anything — guard first, loudly.
        [ $# -ge 2 ] || { usage; die "$1 needs a value"; }
        case "$1" in
          --email) EMAIL="$2" ;;
          --password) PASSWORD="$2" ;;
        esac
        shift 2 ;;
      --test) TEST_MODE=1; shift ;;
      -h|--help) usage; exit 0 ;;
      *) usage; die "unknown flag: $1" ;;
    esac
  done
  if [ "$TEST_MODE" = "1" ]; then
    : "${EMAIL:=test@example.com}"
    : "${PASSWORD:=test-password-123}"
  fi
  [ -n "$EMAIL" ] || { usage; die "--email is required"; }
  [ -n "$PASSWORD" ] || { usage; die "--password is required"; }
  # Same floor the backend enforces; catching it here saves a full install.
  [ "${#PASSWORD}" -ge 8 ] || { usage; die "--password must be at least 8 characters"; }
  # Same shape the server gates boot on (config.Load emailRe): fail fast
  # here with usage, not halfway through an install.
  [[ "$EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] \
    || { usage; die "--email '$EMAIL' is not a valid email address"; }
}

TEST_MODE=0
if [ "${PCODER_SETUP_TEST:-0}" = "1" ]; then
  TEST_MODE=1
fi

# port_taken reports whether anything LISTENs on a TCP port via bash's
# /dev/tcp (Linux + macOS, zero dependencies). A bash without net
# redirections just reports free — same as the old "continuing" fallback.
port_taken() {
  if (exec 3<>"/dev/tcp/127.0.0.1/$1") 2>/dev/null; then
    return 0
  fi
  return 1
}

# resolve_install_dir picks where the checkout lives. Explicit override
# wins; an existing install keeps its location (the writability gate below
# then decides between sudo and override); a fresh root run lands in /opt;
# everyone else falls back under $HOME with a loud log instead of an error.
resolve_install_dir() {
  if [ -n "${PCODER_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$PCODER_INSTALL_DIR"
    return 0
  fi
  INSTALL_DIR=/opt/pocket-coder/src
  [ -e "$INSTALL_DIR" ] && return 0
  [ -w /opt ] 2>/dev/null && return 0
  [ -n "${HOME:-}" ] || die "cannot write to /opt and HOME is unset — set PCODER_INSTALL_DIR to a writable path"
  INSTALL_DIR="$HOME/pocket-coder/src"
  log "no write access to /opt — installing under $INSTALL_DIR (override with PCODER_INSTALL_DIR)"
}

# disk_avail_kb routes by OS — GNU df (--output) exists only on Linux,
# BSD df (-k col 4) only on macOS. Split so a df flag change on one
# platform can never break the other.
disk_avail_kb() {
  if [ "$OS" = "Darwin" ]; then
    disk_avail_kb_mac "$@"
  else
    disk_avail_kb_linux "$@"
  fi
}

disk_avail_kb_linux() {
  disk_dir="$1"
  while [ ! -e "$disk_dir" ]; do disk_dir="$(dirname "$disk_dir")"; done
  disk_out="$(df --output=avail "$disk_dir" 2>/dev/null | tail -1 | tr -d ' ' || true)"
  case "$disk_out" in ''|*[!0-9]*) return 1 ;; *) printf '%s' "$disk_out" ;; esac
}

disk_avail_kb_mac() {
  disk_dir="$1"
  while [ ! -e "$disk_dir" ]; do disk_dir="$(dirname "$disk_dir")"; done
  disk_out="$(df -k "$disk_dir" 2>/dev/null | tail -1 | awk '{print $4}' || true)"
  case "$disk_out" in ''|*[!0-9]*) return 1 ;; *) printf '%s' "$disk_out" ;; esac
}

# 2. validate — nothing is installed or downloaded before this passes.
# Ordered cheapest-first. Distro/arch warn, everything else hards.
# (--test never reaches here: test_main exits right after parse_args.)
# OS-routed: edit one routine without touching the other.
validate() {
  if [ "$OS" = "Darwin" ]; then
    validate_mac
  else
    validate_linux
  fi
}

validate_mac() {
  resolve_install_dir
  probe="$INSTALL_DIR"
  while [ ! -e "$probe" ]; do probe="$(dirname "$probe")"; done
  [ -w "$probe" ] || die "cannot write to $INSTALL_DIR (running as $(id -un)) — re-run with sudo or set PCODER_INSTALL_DIR to a writable path"
  log "macOS detected — Docker Desktop must already be installed and running"
  arch="$(uname -m)"
  case "$arch" in
    x86_64|aarch64|arm64) ;;
    *) log "untested arch ($arch) — docker itself will fail loudly if truly unsupported" ;;
  esac
  # No package-manager gate: a Mac with Docker Desktop and git/curl
  # already present sails straight through.
  if [ -d "$INSTALL_DIR/.git" ]; then
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
  disk_kb="$(disk_avail_kb_mac "$INSTALL_DIR" || true)"
  if [ -n "$disk_kb" ] && [ "$disk_kb" -lt 10485760 ]; then
    die "need at least 10 GB free under $probe (have $((disk_kb / 1024 / 1024)) GB)"
  fi
  # No /proc/meminfo on macOS — warn only, containers want 2 GB.
  log "cannot read RAM size — continuing, project containers want 2 GB"
  log "validations passed"
}

validate_linux() {
  resolve_install_dir
  # No root gate: non-root works when the install dir is writable and the
  # user can reach the docker daemon (docker group). Anything privileged
  # below fails loudly with a sudo-or-fix hint instead.
  probe="$INSTALL_DIR"
  while [ ! -e "$probe" ]; do probe="$(dirname "$probe")"; done
  [ -w "$probe" ] || die "cannot write to $INSTALL_DIR (running as $(id -un)) — re-run with sudo or set PCODER_INSTALL_DIR to a writable path"
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
    x86_64|aarch64) ;;
    *) log "untested arch ($arch) — docker itself will fail loudly if truly unsupported" ;;
  esac
  if [ -d "$INSTALL_DIR/.git" ]; then
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
  disk_kb="$(disk_avail_kb_linux "$INSTALL_DIR" || true)"
  if [ -n "$disk_kb" ] && [ "$disk_kb" -lt 10485760 ]; then
    die "need at least 10 GB free under $probe (have $((disk_kb / 1024 / 1024)) GB)"
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
# the binaries are all present). OS-routed: macOS never touches apt/dnf.
install_deps() {
  if [ "$OS" = "Darwin" ]; then
    install_deps_mac
  else
    install_deps_linux
  fi
}

install_deps_mac() {
  # macOS has no apt/dnf/yum: Docker comes from Docker Desktop, git/curl
  # from Xcode CLT or Homebrew. Never suggest apt here.
  missing_mac=""
  command -v git >/dev/null 2>&1 || missing_mac="$missing_mac git"
  command -v curl >/dev/null 2>&1 || missing_mac="$missing_mac curl"
  [ -z "$missing_mac" ] || die "missing:$missing_mac — install Xcode CLT (xcode-select --install) or Homebrew, then re-run"
  if ! command -v docker >/dev/null 2>&1; then
    if command -v brew >/dev/null 2>&1; then
      die "missing: docker — install it with: brew install --cask docker (then launch Docker Desktop and re-run)"
    fi
    die "missing: docker — install Docker Desktop from https://www.docker.com/products/docker-desktop/ (Apple Silicon build), launch it, then re-run"
  fi
  if [ "${RERUN:-0}" != "1" ]; then
    install_compose_plugin_mac
  fi
  ensure_docker_running_mac
  command -v docker >/dev/null 2>&1 || die "docker install failed — see https://docs.docker.com/desktop/setup/install/mac-install/"
  docker compose version >/dev/null 2>&1 || die "docker compose plugin missing — update Docker Desktop to the latest version"
  log "dependencies installed"
}

install_deps_linux() {
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
      if [ "$(id -u)" != "0" ]; then
        die "missing:$to_install — install them first or re-run with sudo"
      fi
      command -v apt-get >/dev/null 2>&1 || command -v dnf >/dev/null 2>&1 || command -v yum >/dev/null 2>&1 \
        || die "no usable package manager (need apt-get, dnf, or yum)"
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
    install_compose_plugin_linux
  fi
  ensure_docker_running_linux
  command -v git >/dev/null 2>&1 || die "git install failed"
  command -v curl >/dev/null 2>&1 || die "curl install failed"
  command -v docker >/dev/null 2>&1 || die "docker install failed — see https://docs.docker.com/engine/install/"
  docker compose version >/dev/null 2>&1 || die "docker compose plugin missing after install"
  log "dependencies installed"
}

# ensure_docker_running — OS-routed. Linux owns the daemon via
# systemd/dockerd; macOS never touches either (Docker Desktop owns it).
ensure_docker_running() {
  if [ "$OS" = "Darwin" ]; then
    ensure_docker_running_mac
  else
    ensure_docker_running_linux
  fi
}

ensure_docker_running_mac() {
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  die "docker daemon unreachable — launch Docker Desktop (open -a Docker), wait for the whale icon, then re-run"
}

ensure_docker_running_linux() {
  systemctl enable --now docker 2>/dev/null || service docker start 2>/dev/null || true
  if docker info >/dev/null 2>&1; then
    return 0
  fi
  log "daemon not running under init — starting dockerd directly"
  (dockerd > /tmp/pcoder-dockerd.log 2>&1 &)
  # No seq: stock macOS has no GNU coreutils (it ships jot instead), so
  # plain while loops everywhere a counter is needed.
  i=0
  while [ "$i" -lt 12 ]; do
    docker info >/dev/null 2>&1 && return 0
    sleep 5
    i=$((i + 1))
  done
  tail -20 /tmp/pcoder-dockerd.log 2>/dev/null || true
  if [ "$(id -u)" != "0" ]; then
    die "docker daemon unreachable (non-root: is your user in the docker group? sudo usermod -aG docker \$USER, then log back in)"
  fi
  die "docker daemon unreachable"
}

# install_compose_plugin — OS-routed. Pinned version for reproducible
# setups; DOCKER_COMPOSE_VERSION overrides. macOS bundles compose in
# Desktop (bare-CLI fallback downloads a darwin binary to ~/.docker);
# Linux downloads to /usr/libexec (Ubuntu ships no compose plugin).
install_compose_plugin() {
  if [ "$OS" = "Darwin" ]; then
    install_compose_plugin_mac
  else
    install_compose_plugin_linux
  fi
}

install_compose_plugin_mac() {
  docker compose version >/dev/null 2>&1 && return 0
  arch="$(uname -m)"
  case "$arch" in
    x86_64) ;;
    aarch64|arm64) arch="aarch64" ;;
    *) die "unsupported arch for compose plugin download: $arch" ;;
  esac
  ver="${DOCKER_COMPOSE_VERSION:-v2.39.2}"
  dest="$HOME/.docker/cli-plugins/docker-compose"
  mkdir -p "$(dirname "$dest")"
  log "installing compose plugin $ver (darwin-$arch)"
  curl -fsSL "https://github.com/docker/compose/releases/download/${ver}/docker-compose-darwin-${arch}" -o "$dest" \
    || die "compose plugin download failed"
  chmod +x "$dest"
}

install_compose_plugin_linux() {
  docker compose version >/dev/null 2>&1 && return 0
  arch="$(uname -m)"
  case "$arch" in
    x86_64) ;;
    aarch64) ;;
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

# test_main — the --test self-test: pure-local, zero side effects, ~1s.
# Runs parse-adjacent checks in a temp dir, then boot end to end against
# stubbed tools. Mocks: root, distro, disk/RAM/port, apt/dnf, docker,
# clone, password setter, boot. Live boot stays manual per-release.
test_main() {
  INSTALL_DIR="$(mktemp -d)"
  # shellcheck disable=SC2064
  trap "rm -rf '$INSTALL_DIR'" EXIT
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
  probe_reject "missing --email value" --email --password x
  probe_reject "missing --password value" --email a@b.cd --password
  probe_reject "bad email" --email not-an-email --password x
  probe_reject "short password" --email a@b.cd --password short
  probe_reject "unknown flag" --bogus
  # Phase 2: run validate + install_deps against stubbed platform tools.
  # Fake docker (healthy daemon, compose present) and failing init shims
  # go on PATH; the install dir is temp. No side effects, but the real
  # rootless/portability logic executes — the macOS bugs lived exactly
  # here, past what --test used to reach. The stub bin lives inside the
  # temp install dir, so the EXIT trap cleans it even on failure.
  stub="$INSTALL_DIR/stubbin"
  mkdir -p "$stub"
  printf '#!/bin/sh\nexit 0\n' > "$stub/docker"
  chmod +x "$stub/docker"
  for s in systemctl service; do
    printf '#!/bin/sh\nexit 1\n' > "$stub/$s"
    chmod +x "$stub/$s"
  done
  PATH="$stub:$PATH" INSTALL_DIR="$INSTALL_DIR/plat" validate \
    || die "test: validate failed with stubbed tools"
  PATH="$stub:$PATH" INSTALL_DIR="$INSTALL_DIR/plat" install_deps \
    || die "test: install_deps failed with stubbed tools"
  # Phase 3 (non-root only — root bypasses permission bits): an explicitly
  # unwritable install dir must fail LOUDLY with the PCODER_INSTALL_DIR
  # hint. (Set via env: validate resolves the location from it.)
  if [ "$(id -u)" != "0" ]; then
    if out="$( (PCODER_INSTALL_DIR=/proc/cant-write-here validate) 2>&1 )"; then
      die "test: validate on unwritable dir exited 0, want failure"
    else
      case "$out" in
        *PCODER_INSTALL_DIR*) ;;
        *) die "test: unwritable dir printed no hint (output was: $out)" ;;
      esac
    fi
  else
    log "test: skipping unwritable-dir probe (running as root)"
  fi
  # Phase 4: resolve_install_dir picks the location. An explicit override
  # is always honored; without one, an unwritable /opt falls back under
  # $HOME (only asserted where /opt is actually unwritable — elsewhere
  # the /opt default correctly wins).
  out="$( (PCODER_INSTALL_DIR=/custom/path resolve_install_dir >/dev/null; printf '%s' "$INSTALL_DIR") )"
  [ "$out" = "/custom/path" ] || die "test: explicit PCODER_INSTALL_DIR not honored (got: $out)"
  if [ ! -w /opt ] && [ -n "${HOME:-}" ]; then
    fakehome="$INSTALL_DIR/fakehome"
    mkdir -p "$fakehome"
    out="$( (HOME="$fakehome"; unset PCODER_INSTALL_DIR; INSTALL_DIR=/opt/pocket-coder/src; resolve_install_dir >/dev/null; printf '%s' "$INSTALL_DIR") )"
    [ "$out" = "$fakehome/pocket-coder/src" ] || die "test: /opt fallback wrong (got: $out)"
  else
    log "test: skipping /opt-fallback probe (/opt is writable here)"
  fi
  # Phase 5: boot() end-to-end with stubbed git/docker/curl/sleep plus a
  # call log. No images, no daemon, no network — but the real sequencing,
  # the health retry loop, and every failure exit execute. Switches
  # (exported so the stub processes see them): PCODER_TEST_PASSWORD_FAIL=1
  # kills the password setter, PCODER_TEST_HEALTH_FAIL=1 fails every
  # health poll, PCODER_TEST_HEALTH_OK_AFTER=N succeeds on the Nth poll.
  cat > "$stub/git" <<'EOF'
#!/bin/sh
echo "git $*" >> "$PCODER_TEST_LOG"
if [ "$1" = "clone" ]; then
  last=""; for a in "$@"; do last="$a"; done
  mkdir -p "$last/.git"
fi
exit 0
EOF
  cat > "$stub/docker" <<'EOF'
#!/bin/sh
echo "docker $*" >> "$PCODER_TEST_LOG"
if [ "${1:-} ${2:-}" = "compose run" ]; then
  # The setter reads the password on stdin; drain it so the pipe closes.
  cat > /dev/null
  [ "${PCODER_TEST_PASSWORD_FAIL:-0}" = "1" ] && exit 1
fi
exit 0
EOF
  cat > "$stub/curl" <<'EOF'
#!/bin/sh
echo "curl $*" >> "$PCODER_TEST_LOG"
case "$*" in
  *localhost:8080/health*)
    if [ "${PCODER_TEST_HEALTH_FAIL:-0}" = "1" ]; then exit 22; fi
    n=0
    [ -f "$PCODER_TEST_COUNT" ] && n="$(cat "$PCODER_TEST_COUNT")"
    n=$((n + 1))
    printf '%s' "$n" > "$PCODER_TEST_COUNT"
    [ "$n" -ge "${PCODER_TEST_HEALTH_OK_AFTER:-1}" ] && exit 0
    exit 22 ;;
  *) exit 22 ;;
esac
EOF
  for s in git docker curl; do chmod +x "$stub/$s"; done
  for s in sleep hostname ipconfig; do
    printf '#!/bin/sh\nexit 0\n' > "$stub/$s"
    chmod +x "$stub/$s"
  done
  # Exported (not command-scoped): every boot probe below must hit the
  # stubs, never the real toolchain — a leak here builds real images.
  export PATH="$stub:$PATH"
  export PCODER_TEST_LOG="$INSTALL_DIR/boottest/calls.log"
  export PCODER_TEST_COUNT="$INSTALL_DIR/boottest/health.count"
  mkdir -p "$INSTALL_DIR/boottest"
  order_ok() {
    prev=0
    for pat in "$@"; do
      cur=$(grep -n "$pat" "$PCODER_TEST_LOG" | head -1 | cut -d: -f1)
      [ -n "$cur" ] && [ "$cur" -gt "$prev" ] || return 1
      prev=$cur
    done
  }
  # Happy path, fresh clone, health green on the 3rd poll.
  export PCODER_TEST_HEALTH_OK_AFTER=3
  : > "$PCODER_TEST_LOG"
  rm -f "$PCODER_TEST_COUNT"
  out="$( (INSTALL_DIR="$INSTALL_DIR/bootroot" boot) )" \
    || die "test: boot happy path failed"
  case "$out" in *"is live"*) ;; *) die "test: boot printed no live banner" ;; esac
  [ ! -e "$INSTALL_DIR/bootroot/server/.env" ] || die "test: boot still writes a .env"
  grep -q "set-password --email $EMAIL" "$PCODER_TEST_LOG" \
    || die "test: boot did not pass the email to set-password"
  [ "$(grep -c "localhost:8080/health" "$PCODER_TEST_LOG")" = "3" ] \
    || die "test: health retry loop polled wrong number of times"
  order_ok "git clone" "compose build" "pcoder-project" "pcoder-browser" "set-password" "compose up" \
    || die "test: boot ran steps out of order"
  # The password must never reach the call log (docker args or output).
  if grep -q "$PASSWORD" "$PCODER_TEST_LOG"; then
    die "test: password leaked into the docker call log"
  fi
  # No install/update path may drop the named data volume.
  if grep -q "volume rm" "$PCODER_TEST_LOG"; then
    die "test: boot removed a volume"
  fi
  unset PCODER_TEST_HEALTH_OK_AFTER
  # Update path: existing checkout fetches, never re-clones.
  mkdir -p "$INSTALL_DIR/bootroot2/.git"
  : > "$PCODER_TEST_LOG"
  ( INSTALL_DIR="$INSTALL_DIR/bootroot2" boot ) >/dev/null \
    || die "test: boot update path failed"
  grep -q " fetch " "$PCODER_TEST_LOG" || die "test: update path skipped fetch"
  grep -q " clone " "$PCODER_TEST_LOG" && die "test: update path cloned over existing repo"
  # A failed credential write aborts before boot, loudly.
  export PCODER_TEST_PASSWORD_FAIL=1
  : > "$PCODER_TEST_LOG"
  if out="$( (INSTALL_DIR="$INSTALL_DIR/bootroot3" boot) 2>&1 )"; then
    die "test: boot with a failed password setter exited 0, want failure"
  else
    case "$out" in
      *"setting the login password failed"*) ;;
      *) die "test: password failure misreported (output was: $out)" ;;
    esac
  fi
  grep -q "compose up" "$PCODER_TEST_LOG" && die "test: boot launched the stack after a failed credential write"
  unset PCODER_TEST_PASSWORD_FAIL
  # Unhealthy server: all 60 polls, diagnostics, then the loud exit.
  export PCODER_TEST_HEALTH_FAIL=1
  : > "$PCODER_TEST_LOG"
  if out="$( (INSTALL_DIR="$INSTALL_DIR/bootroot4" boot) 2>&1 )"; then
    die "test: boot with dead server exited 0, want failure"
  else
    case "$out" in
      *"never became healthy"*) ;;
      *) die "test: health timeout misreported (output was: $out)" ;;
    esac
  fi
  unset PCODER_TEST_HEALTH_FAIL
  grep -q "compose logs" "$PCODER_TEST_LOG" || die "test: health timeout dumped no diagnostics"
  [ "$(grep -c "localhost:8080/health" "$PCODER_TEST_LOG")" = "60" ] \
    || die "test: health loop gave up early"
  log "TEST MODE PASS — arg parsing, validation, install-dir, and stubbed boot all good (live boot stays manual per-release)"
  trap - EXIT
  rm -rf "$INSTALL_DIR"
}

# get_ip — OS-routed. Linux uses EC2 metadata then hostname -I;
# macOS uses EC2 metadata (fails fast off-cloud) then ipconfig en0/en1.
# Split so hostname/ipconfig flag changes stay on their own platform.
get_ip() {
  if [ "$OS" = "Darwin" ]; then
    get_ip_mac
  else
    get_ip_linux
  fi
}

get_ip_linux() {
  ip="$(curl -fsS --max-time 5 http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true)"
  [ -n "$ip" ] || ip="$(hostname -I 2>/dev/null | awk '{print $1}' || true)"
  [ -n "$ip" ] || ip="<this-host>"
  printf '%s' "$ip"
}

get_ip_mac() {
  ip="$(curl -fsS --max-time 5 http://169.254.169.254/latest/meta-data/public-ipv4 2>/dev/null || true)"
  # First active Wi-Fi/Ethernet interface wins (en0 empty on some Macs).
  if [ -z "$ip" ]; then
    for iface in en0 en1; do
      ip="$(ipconfig getifaddr "$iface" 2>/dev/null || true)"
      [ -n "$ip" ] && break
    done
  fi
  [ -n "$ip" ] || ip="<this-host>"
  printf '%s' "$ip"
}

# 4. boot — fetch/pull → build → set-password → up → wait healthy → banner.
# Update-safe: the same curl command re-runs on an existing install. The
# pcoder-data volume is never removed here: projects, the login email, and
# the stored password hash survive an update.
boot() {
  if [ -d "$INSTALL_DIR/.git" ]; then
    log "repo exists — fetching $REPO_BRANCH"
    git -C "$INSTALL_DIR" fetch --depth 1 origin "$REPO_BRANCH"
    git -C "$INSTALL_DIR" reset --hard "origin/$REPO_BRANCH"
  else
    log "cloning $REPO_URL ($REPO_BRANCH)"
    if ! git clone --depth 1 --branch "$REPO_BRANCH" "$REPO_URL" "$INSTALL_DIR"; then
      die "clone failed"
    fi
  fi
  cd "$INSTALL_DIR"
  # The stack network is external (declared, not managed): ensure it
  # before the stack uses it.
  docker network create pcoder-net >/dev/null 2>&1 || true
  log "building images (several minutes on first run)"
  docker compose build
  # Pre-build on-demand runtime images so the first clone/preview doesn't
  # block on a 2-5 min Docker build inside the HTTP request — that was the
  # original 504 Gateway Timeout source. The tags must match the constants
  # in server/internal/project/service.go and server/internal/preview/docker_runtime.go;
  # a drift guard test (cmd/server) fails the build when they stop matching.
  docker build -t pcoder-project:v4 "$INSTALL_DIR/server/internal/project/image"
  docker build -t pcoder-browser:v14 "$INSTALL_DIR/server/internal/preview/image"
  # The credential write needs the stack stopped: the setter rewrites
  # state.json, and a live server would clobber it from memory.
  docker compose down >/dev/null 2>&1 || true
  log "setting the login password"
  # The image ENTRYPOINT is already ["pcoder"], so the subcommand is
  # appended bare (not `pcoder set-password`, which would repeat the
  # binary). --rm: one-shot. -T: no TTY, so stdin is the pipe. The
  # password travels on stdin — never an argument, never persisted (only
  # the hash is, in state.json).
  if ! printf '%s' "$PASSWORD" | docker compose run --rm -T server set-password --email "$EMAIL"; then
    die "setting the login password failed — re-run this script"
  fi
  log "booting the stack"
  docker compose up -d
  log "waiting for /health (120s)"
  i=0
  while [ "$i" -lt 60 ]; do
    if curl -fsS http://localhost:8080/health >/dev/null 2>&1; then
      break
    fi
    sleep 2
    i=$((i + 1))
    if [ "$i" = "60" ]; then
      docker compose logs --tail=50 server || true
      die "server never became healthy — logs above"
    fi
  done
  ip="$(get_ip)"
  [ -n "$ip" ] || ip="<this-host>"
  cat <<EOF

[setup] done — Pocket Coder is live:
[setup]   URL:   http://$ip:8080
[setup]   login: $EMAIL (the password you passed to this script)
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
