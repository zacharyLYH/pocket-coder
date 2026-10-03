#!/bin/sh
# Run the e2e suite as PARALLEL PROCESSES, one per test type. Each process
# gets its own backend port, frontend port, and test-results subdirectory
# (E2E_API_PORT / E2E_WEB_PORT / E2E_RUN_ID — see e2e/env.ts), so groups
# share nothing: separate state.json, separate PIN log, separate auth state.
#
# Groups start a few seconds apart so their first project creations don't
# race each other's one-time project-image build on a cold engine.
#
# A trap guarantees that `docker compose down` runs for every group on EXIT,
# even when the shell is interrupted or a syntax error aborts the script —
# so Playwright processes that are killed mid-run can't leave pcoder-e2e-<group>
# server containers behind on the engine.
#
# Usage:
#   sh e2e-parallel.sh                       # all groups in parallel
#   sh e2e-parallel.sh stack sessions        # only the named groups
#   UPDATE_SNAPSHOTS=1 sh e2e-parallel.sh    # regenerate all baseline shots
set -u
cd "$(dirname "$0")"

mkdir -p test-results

# Ensure web deps + the pinned Playwright runner are installed. Without
# node_modules `npx playwright` fetches the latest standalone `playwright`
# package, which mismatches @playwright/test and breaks flag parsing.
if [ ! -x node_modules/.bin/playwright ]; then
  npm install
fi

# All E2E test groups.  The name is the E2E_RUN_ID and compose project
# suffix, the offset maps to distinct api/web ports (8080+offset*10), and
# the rest is the spec list passed to `npx playwright test`.
#
# Four preview groups, rebalanced so the slowest npm-install previews run
# isolated and the fast static/no-npm ones are spread to balance wall-clock
# (estimates: npm install + Vite boot ~30-45s, static fixture ~15s,
# screenshot ~5s):
#
#   tools      ~180s  6 tests, 6 React projects    (isolated — slowest)
#   token      ~130s  4 tests, React + token waits  (isolated)
#   journey     ~75s  3 preinstalled + 9 screenshots
#   viewport    ~60s  2 React projects
#   auth        ~40s  1 React project (cross-project isolation)
#   htmx        ~40s  1 Vite + 2 screenshots
#   vue         ~40s  1 Vite + 2 screenshots
#   reconnect   ~35s  1 React + 1 screenshot
#   port        ~30s  1 React (non-default-port)
#   shortcuts   ~15s  3 tests, no npm install
#   fit         ~15s  1 static + 2 screenshots
#   vanilla     ~15s  1 static (no Vite/npm)
# Group estimates:
#   preview.a  ~180s  tools
#   preview.b  ~160s  token + fit + vanilla
#   preview.c  ~175s  journey + auth + viewport
#   preview.d  ~160s  htmx + vue + reconnect + port + shortcuts
# The bootstrap group boots from a SEEDED state.json (E2E_SEED=bootstrap,
# materialized by the seeder factory e2e/stateSeed.ts): state has a
# project with opencode recorded while Docker is empty, so the server must
# finish its boot bootstrap (container + harness install) before serving.
ALL_GROUPS='
app|1|e2e/app.spec.ts e2e/bugreport.spec.ts e2e/preview.basic.spec.ts e2e/preview.hmr.spec.ts
stack|2|e2e/terminal.stack.spec.ts
sessions|3|e2e/terminal.session.spec.ts e2e/terminal.mobile.spec.ts e2e/nerdy.mobile.spec.ts e2e/nerdy.desktop.spec.ts e2e/harness.inject.spec.ts e2e/harness.orchestration.spec.ts e2e/harness.relaunch.spec.ts
visual|4|e2e/home.visual.spec.ts e2e/settings.spec.ts e2e/codemap.spec.ts e2e/butler.spec.ts e2e/butler.live.spec.ts e2e/butler.confirm.spec.ts e2e/pulltorefresh.mobile.spec.ts
preview.a|5|e2e/preview.tools.spec.ts
preview.b|6|e2e/preview.token.spec.ts e2e/preview.fit.spec.ts e2e/preview.vanilla.spec.ts
preview.c|7|e2e/preview.journey.spec.ts e2e/preview.auth.spec.ts e2e/preview.viewport.spec.ts
preview.d|8|e2e/preview.htmx.spec.ts e2e/preview.vue.spec.ts e2e/preview.reconnect.spec.ts e2e/preview.port.spec.ts e2e/shortcuts.spec.ts
bootstrap|9|e2e/bootstrap.spec.ts
'

# Groups we started during this run — for the EXIT trap.
STARTED_GROUPS=""

# Background PIDs per group ("name:pid"), for the live dashboard.
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
  # bootstrap boots from a seeded state.json (see ALL_GROUPS above)
  seed=""
  [ "$name" = "bootstrap" ] && seed="E2E_SEED=bootstrap"
  # Clean up any leftover containers from crashed runs. down matches by
  # project name; the data-dir/port vars only need to parse, so dummy
  # values are fine (real values ride with the test process env below).
  # Without them compose exits 1 on the blank :/data mount and the cleanup
  # silently does nothing.
  PCODER_E2E_DATA_DIR="${PCODER_E2E_DATA_DIR:-/tmp/pcoder-e2e-down}" PCODER_E2E_API_PORT="${PCODER_E2E_API_PORT:-8080}" \
  docker compose -f ../docker-compose.yml -f ../docker-compose.e2e.yml -p "$(proj_name "$name")" down 2>/dev/null
  echo "[$name] starting: api:$api web:$web git:$git → test-results/$name.log + test-results/$name/progress.md"
  # NOTE: reporters must be comma-separated in ONE --reporter flag. Repeated
  # --reporter flags load both reporters but silently swallow the list
  # reporter's stdout whenever output is piped (all .log files stay 0 bytes).
  env E2E_RUN_ID="$name" E2E_API_PORT="$api" E2E_WEB_PORT="$web" E2E_GIT_PORT="$git" $seed \
    npx playwright test --config=playwright.config.ts --reporter=list,./e2e/progress-reporter.ts ${UPDATE_SNAPSHOTS:+--update-snapshots=all} $specs > "test-results/$name.log" 2>&1 &
  GROUP_PIDS="$GROUP_PIDS $name:$!"
  STARTED_GROUPS="$STARTED_GROUPS $name"
}

# Tear down every group's compose project. Runs on interruption (Ctrl-C,
# kill) and — via explicit call at the end — on normal exit. EXIT is
# deliberately NOT trapped during `wait`: bash 3.2 (macOS /bin/sh) has a
# run_pending_traps bug where `trap ... EXIT` + `wait <pid>` returns
# immediately with "bad value in trap_list[15]", so the script would check
# still-empty logs and falsely report "all groups passed" while Playwright
# was still booting.
cleanup_all() {
  kill "${AGG_PID:-}" 2>/dev/null || true
  for name in $STARTED_GROUPS; do
    # Dummy vars: see the pre-run cleanup above — down needs them to parse.
    PCODER_E2E_DATA_DIR="${PCODER_E2E_DATA_DIR:-/tmp/pcoder-e2e-down}" PCODER_E2E_API_PORT="${PCODER_E2E_API_PORT:-8080}" \
    docker compose -f ../docker-compose.yml -f ../docker-compose.e2e.yml -p "$(proj_name "$name")" down 2>/dev/null || true
  done
}
trap cleanup_all INT TERM

WANTED="${*:-}"
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

# ── live progress file ────────────────────────────────────────────────
# One combined test-results/progress.md, rebuilt every 2s from the groups'
# per-test checklists. File only — nothing is printed while groups run, so
# just open test-results/progress.md in an editor and watch it update.
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
  for name in app stack sessions visual preview.a preview.b preview.c preview.d bootstrap; do
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

# Wait for the test groups only (not the aggregator above), tracking
# per-group exit codes — `wait $pids` as one call masks which group failed
# and, on bash 3.2, trips the EXIT-trap bug above.
WAIT_PIDS=""
for entry in $GROUP_PIDS; do
  WAIT_PIDS="$WAIT_PIDS ${entry#*:}"
done
group_fail=0
# shellcheck disable=SC2086
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
for name in app stack sessions visual preview.a preview.b preview.c preview.d bootstrap; do
  case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
  log="test-results/$name.log"
  prog="test-results/$name/progress.md"
  tail -n 3 "$log" 2>/dev/null | sed "s/^/[$name] /"
  group_bad=""
  # An empty/missing log means Playwright never ran (killed before flush,
  # wrapper raced ahead) — must fail, never silently pass.
  if [ ! -s "$log" ]; then
    echo "[$name] EMPTY log — Playwright produced no output"
    group_bad=1
  fi
  # Zero tests discovered means setup/boot failed (see progress.md 0/0).
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
  # On failure, dump the relevant error lines straight to stdout so the
  # user doesn't have to open the log file. Trigger ONLY on Playwright's
  # own failure markers (✘ lines, "N failed" footer): the log also carries
  # benign noise like vite "[WebServer] Error: write EPIPE" and test names
  # containing "failed", which must not fail the run (45 passed + EPIPE
  # noise == green).
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
