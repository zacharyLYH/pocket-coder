#!/bin/sh
# E2E suite: one Playwright process per group, each with its own ports and
# test-results dir (see e2e/env.ts). Silent while running — watch
# test-results/progress.md.
#
# Usage:
#   sh e2e-parallel.sh                    # all groups
#   sh e2e-parallel.sh stack sessions     # only these groups
#   UPDATE_SNAPSHOTS=1 sh e2e-parallel.sh # regenerate baselines
set -u
cd "$(dirname "$0")"

mkdir -p test-results

# Preflight: missing deps and squatted ports used to surface minutes later
# as a confusing 0-test "pass". Fail here instead, with the fix attached.
MISSING=0
need() {
  command -v "$1" >/dev/null 2>&1 || { echo "e2e-parallel.sh: missing '$1' — $2" >&2; MISSING=1; }
}
need node "install Node LTS from https://nodejs.org"
need npm "ships with Node — reinstall Node if absent"
need docker "install https://docs.docker.com/desktop, then start it"
need git "install from https://git-scm.com/downloads"
[ "$MISSING" -eq 0 ] || exit 2
docker info >/dev/null 2>&1 || {
  echo "e2e-parallel.sh: Docker engine not reachable — start Docker Desktop, then retry" >&2
  exit 2
}
docker compose version >/dev/null 2>&1 || {
  echo "e2e-parallel.sh: 'docker compose' (v2 plugin) not found — update Docker Desktop" >&2
  exit 2
}

# node_modules must be the repo-pinned tree: bare `npx playwright` fetches
# the latest standalone runner, which mismatches @playwright/test.
if [ ! -x node_modules/.bin/playwright ]; then
  echo "e2e-parallel.sh: node_modules missing — running 'npm install'..."
  npm install || { echo "e2e-parallel.sh: 'npm install' failed" >&2; exit 2; }
fi

# name|port-offset|specs. Offsets map to api/web/git ports (8080/5170/9070 + offset*10).
ALL_GROUPS='
app|1|e2e/app.spec.ts e2e/bugreport.spec.ts e2e/preview.basic.spec.ts e2e/preview.hmr.spec.ts
stack|2|e2e/terminal.stack.spec.ts
sessions|3|e2e/terminal.session.spec.ts e2e/terminal.mobile.spec.ts e2e/nerdy.mobile.spec.ts e2e/nerdy.desktop.spec.ts e2e/harness.inject.spec.ts e2e/harness.orchestration.spec.ts e2e/harness.relaunch.spec.ts
visual|4|e2e/home.visual.spec.ts e2e/settings.spec.ts e2e/codemap.spec.ts e2e/butler.spec.ts e2e/butler.live.spec.ts e2e/butler.confirm.spec.ts e2e/pulltorefresh.mobile.spec.ts
preview.a|5|e2e/preview.tools.spec.ts
preview.b|6|e2e/preview.token.spec.ts
preview.c|7|e2e/preview.journey.spec.ts e2e/preview.auth.spec.ts e2e/preview.viewport.spec.ts
preview.d|8|e2e/preview.htmx.spec.ts e2e/preview.vue.spec.ts e2e/preview.reconnect.spec.ts e2e/preview.port.spec.ts e2e/shortcuts.spec.ts
preview.e|10|e2e/preview.fit.spec.ts e2e/preview.vanilla.spec.ts
bootstrap|9|e2e/bootstrap.spec.ts
'

WANTED="${*:-}"
# Unknown names must error, not silently run zero groups (a vacuous "pass").
NL="$(printf '\nx')"; NL="${NL%x}"
for w in $WANTED; do
  case "$NL$ALL_GROUPS" in
    *"$NL$w|"*) ;;
    *) echo "e2e-parallel.sh: unknown group '$w' — valid: app stack sessions visual preview.a preview.b preview.c preview.d preview.e bootstrap" >&2; exit 2 ;;
  esac
done

# TCP probe via node (portable; lsof and /dev/tcp aren't everywhere).
port_in_use() {
  node -e "const s=require('net').connect($1,'127.0.0.1');s.on('connect',()=>{s.end();process.exit(0)});s.on('error',()=>process.exit(1));setTimeout(()=>process.exit(1),1500).unref()" 2>/dev/null
}
# A squatted port kills the group in seconds with 0 tests — check before
# launching anything, while the fix is one command.
while IFS='|' read -r name offset _specs; do
  [ -z "$name" ] && continue
  case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
  for p in $((8080 + offset * 10)) $((5170 + offset * 10)) $((9070 + offset * 10)); do
    if port_in_use "$p"; then
      echo "e2e-parallel.sh: port $p (group '$name') is already in use — likely a leftover container from a killed run." >&2
      echo "  Clean up with: docker rm -f \$(docker ps -aq --filter name=pcoder-)" >&2
      exit 2
    fi
  done
done <<EOF
$(printf '%s\n' "$ALL_GROUPS")
EOF

# Groups started so far — the INT/TERM trap tears their stacks down.
STARTED_GROUPS=""

# Background PIDs per group (name:pid).
GROUP_PIDS=""
RUN_EPOCH=""

# Compose project name for a group. Must mirror env.ts COMPOSE_PROJECT:
# run ids like "preview.a" are sanitized because compose v2 rejects dots in
# project names.
proj_name() {
  echo "pcoder-e2e-$(echo "$1" | tr '.' '-')"
}

run_group() {
  name="$1"; offset="$2"; specs="$3"
  api=$((8080 + offset * 10))
  web=$((5170 + offset * 10))
  git=$((9070 + offset * 10))
  # bootstrap boots from a seeded state.json (E2E_SEED=bootstrap).
  seed=""
  [ "$name" = "bootstrap" ] && seed="E2E_SEED=bootstrap"
  # Drop leftovers from crashed runs. Dummy vars: down only needs to parse
  # the compose files here (real values ride with the test process below).
  PCODER_E2E_DATA_DIR="${PCODER_E2E_DATA_DIR:-/tmp/pcoder-e2e-down}" PCODER_E2E_API_PORT="${PCODER_E2E_API_PORT:-8080}" \
  docker compose -f ../docker-compose.yml -f ../docker-compose.e2e.yml -p "$(proj_name "$name")" down 2>/dev/null
  echo "[$name] starting: api:$api web:$web git:$git → test-results/$name.log + test-results/$name/progress.md"
  # One --reporter flag, comma-separated: repeated flags swallow stdout when piped (0-byte logs).
  env E2E_RUN_ID="$name" E2E_API_PORT="$api" E2E_WEB_PORT="$web" E2E_GIT_PORT="$git" $seed \
    npx playwright test --config=playwright.config.ts --reporter=list,./e2e/progress-reporter.ts ${UPDATE_SNAPSHOTS:+--update-snapshots=all} $specs > "test-results/$name.log" 2>&1 &
  GROUP_PIDS="$GROUP_PIDS $name:$!"
  STARTED_GROUPS="$STARTED_GROUPS $name"
}

# Down every started stack. Trapped on INT/TERM; called explicitly at the
# end. Never trap EXIT: bash 3.2 (macOS /bin/sh) returns from `wait` early
# when an EXIT trap is set, which once caused false "all groups passed".
cleanup_all() {
  kill "${AGG_PID:-}" 2>/dev/null || true
  for name in $STARTED_GROUPS; do
    PCODER_E2E_DATA_DIR="${PCODER_E2E_DATA_DIR:-/tmp/pcoder-e2e-down}" PCODER_E2E_API_PORT="${PCODER_E2E_API_PORT:-8080}" \
    docker compose -f ../docker-compose.yml -f ../docker-compose.e2e.yml -p "$(proj_name "$name")" down 2>/dev/null || true
  done
}
trap cleanup_all INT TERM

while IFS='|' read -r name offset specs; do
  [ -z "$name" ] && continue
  case " $WANTED " in
    *" $name "*|"  ") ;;
    *) continue ;;
  esac
  run_group "$name" "$offset" "$specs"
  sleep 4 # stagger image-build / git-daemon startup races
done <<EOF
$(printf '%s\n' "$ALL_GROUPS")
EOF

# ── live progress: test-results/progress.md, rebuilt every 2s. ──────────
# File only — open it in an editor and watch it update while groups run.
group_running() {
  for entry in $GROUP_PIDS; do
    if [ "${entry%%:*}" = "$1" ]; then
      kill -0 "${entry#*:}" 2>/dev/null && return 0
      return 1
    fi
  done
  return 1
}

aggregate_progress() {
  # Command substitution strips trailing newlines, so mint a reusable one.
  NL="$(printf '\nx')"; NL="${NL%x}"
  elapsed=$(($(date +%s) - RUN_EPOCH))
  out=""
  for name in app stack sessions visual preview.a preview.b preview.c preview.d preview.e bootstrap; do
    case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
    prog="test-results/$name/progress.md"
    if group_running "$name"; then state="running"; else state="done"; fi
    if [ -f "$prog" ]; then
      done_n=$(grep -c '^- \[x\]' "$prog" 2>/dev/null || true)
      fail_n=$(grep -c '^- \[!\]' "$prog" 2>/dev/null || true)
      total_n=$(grep -c '^- \[' "$prog" 2>/dev/null || true)
      out="${out}## [$name] $state — $done_n/$total_n passed, $fail_n failed$NL$NL"
      out="${out}$(grep '^- \[' "$prog" 2>/dev/null)$NL$NL"
    else
      boot_line=$(tail -n 1 "test-results/$name.log" 2>/dev/null || true)
      out="${out}## [$name] $state — booting…$NL$NL  $boot_line$NL$NL"
    fi
  done
  printf '# e2e progress — %ss elapsed\n\n%s' "$elapsed" "$out" > test-results/progress.md
}

RUN_EPOCH=$(date +%s)
aggregate_progress
while :; do sleep 2; aggregate_progress; done &
AGG_PID=$!

# Wait per group so the exit code says WHICH group failed. (`wait` on all
# pids at once masks that — and on bash 3.2 trips the EXIT-trap bug above.)
group_fail=0
for entry in $GROUP_PIDS; do
  gname="${entry%%:*}"
  gpid="${entry#*:}"
  if wait "$gpid"; then
    :
  else
    echo "[$gname] playwright process exited $?"
    group_fail=1
  fi
done
kill "$AGG_PID" 2>/dev/null || true
aggregate_progress

echo
echo "live checklist: test-results/progress.md"
fail=$group_fail
for name in app stack sessions visual preview.a preview.b preview.c preview.d preview.e bootstrap; do
  case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
  log="test-results/$name.log"
  prog="test-results/$name/progress.md"
  tail -n 3 "$log" 2>/dev/null | sed "s/^/[$name] /"
  group_bad=""
  # Empty log = Playwright never ran; zero tests = boot/setup failed.
  # Either must fail, never silently pass.
  if [ ! -s "$log" ]; then
    echo "[$name] EMPTY log — Playwright produced no output"
    group_bad=1
  fi
  if [ -f "$prog" ]; then
    total_n=$(grep -c '^- \[' "$prog" 2>/dev/null || true)
    fail_n=$(grep -c '^- \[!\]' "$prog" 2>/dev/null || true)
    total_n="${total_n:-0}"
    fail_n="${fail_n:-0}"
    if [ "$total_n" -eq 0 ]; then
      echo "[$name] no tests recorded in $prog"
      group_bad=1
    fi
    if [ "$fail_n" -ne 0 ]; then
      group_bad=1
    fi
  else
    echo "[$name] missing $prog"
    group_bad=1
  fi
  if [ -n "$group_bad" ]; then
    fail=1
  fi
  # Dump failures inline. Trigger only on Playwright's own markers (✘, "N
  # failed"): the log also holds benign noise (vite EPIPE, "failed" in test
  # names) that must not fail a green run.
  if grep -qE "✘[[:space:]]+[0-9]+|[0-9]+ failed" "$log" 2>/dev/null; then
    echo
    echo "─── [$name] failures — see full log in test-results/$name.log ───"
    grep -E "✘|rejected|Error:|AssertionError|expect\(|[0-9]+ failed|[0-9]+ passed" "$log" | head -20 | sed "s/^/[$name] /"
    echo "─── end [$name] ───"
    fail=1
  fi
done

if [ "$fail" -ne 0 ]; then
  echo
  echo "PARALLEL E2E: FAILED — failure details above, full per-group output in test-results/<group>.log"
  cleanup_all
  exit 1
fi

cleanup_all
echo "PARALLEL E2E: all groups passed"
