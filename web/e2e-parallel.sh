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
set -u
cd "$(dirname "$0")"

mkdir -p test-results

# All E2E test groups.  The name is the E2E_RUN_ID and compose project
# suffix, the offset maps to distinct api/web ports (8080+offset*10), and
# the rest is the spec list passed to `npx playwright test`.
#
# Three preview groups, sized so no group dominates the wall-clock
# (estimates: npm install + Vite boot ~30s, preinstalled+shots ~10s,
#  screenshot ~5s):
#
#   tools      ~180s  6 tests, 6 React projects
#   journey     ~75s  3 preinstalled + 9 screenshots
#   viewport    ~60s  2 React projects
#   auth        ~60s  2 React projects
#   port        ~30s  1 React (non-default-port)
#   htmx        ~40s  1 Vite + 2 screenshots
#   vue         ~40s  1 Vite + 2 screenshots
#   reconnect   ~35s  1 React + 1 screenshot
#   fit         ~15s  1 static + 2 screenshots
#   vanilla     ~15s  1 static (no Vite/npm)
#   quickcmds   ~10s  1 blank project, API-only
#   bootstrap   ~120s 1 seeded project (opencode download at boot)
# The bootstrap group boots from a SEEDED state.json (E2E_SEED): state has a
# project with opencode recorded while Docker is empty, so the server must
# finish its boot bootstrap (container + harness install) before serving.
ALL_GROUPS='
app|1|e2e/app.spec.ts e2e/preview.basic.spec.ts e2e/preview.hmr.spec.ts e2e/sshkeys.e2e.spec.ts
stack|2|e2e/terminal.stack.spec.ts
sessions|3|e2e/terminal.session.spec.ts e2e/harness.inject.spec.ts e2e/harness.orchestration.spec.ts e2e/harness.relaunch.spec.ts
visual|4|e2e/home.visual.spec.ts
preview.a|5|e2e/preview.tools.spec.ts
preview.b|6|e2e/preview.journey.spec.ts e2e/preview.auth.spec.ts e2e/preview.port.spec.ts
preview.c|7|e2e/preview.viewport.spec.ts e2e/preview.fit.spec.ts e2e/preview.htmx.spec.ts e2e/preview.vue.spec.ts e2e/preview.reconnect.spec.ts e2e/preview.vanilla.spec.ts e2e/quickcommands.spec.ts
bootstrap|8|e2e/bootstrap.spec.ts
'

# Groups we started during this run — for the EXIT trap.
STARTED_GROUPS=""

run_group() {
  name="$1"; offset="$2"; specs="$3"
  api=$((8080 + offset * 10))
  web=$((5170 + offset * 10))
  # bootstrap boots from a seeded state.json (see ALL_GROUPS above)
  seed=""
  [ "$name" = "bootstrap" ] && seed="E2E_SEED=bootstrap.seed.json"
  # Clean up any leftover containers from crashed runs
  docker compose -f ../docker-compose.e2e.yml -p "pcoder-e2e-$name" down 2>/dev/null
  echo "[$name] starting: api:$api web:$web → test-results/$name.log"
  env E2E_RUN_ID="$name" E2E_API_PORT="$api" E2E_WEB_PORT="$web" $seed \
    npx playwright test --config=playwright.config.ts $specs > "test-results/$name.log" 2>&1 &
  STARTED_GROUPS="$STARTED_GROUPS $name"
}

# Tear down every group's compose project. Runs on normal EXIT and on
# interruption (Ctrl-C, kill, syntax error in a subshell, etc.).
cleanup_all() {
  for name in $STARTED_GROUPS; do
    docker compose -f ../docker-compose.e2e.yml -p "pcoder-e2e-$name" down 2>/dev/null
  done
}
trap cleanup_all EXIT INT TERM

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

wait

echo
fail=0
for name in app stack sessions visual preview.a preview.b preview.c bootstrap; do
  case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
  log="test-results/$name.log"
  tail -n 3 "$log" | sed "s/^/[$name] /"
  # On failure, dump the relevant error lines straight to stdout so the
  # user doesn't have to open the log file. Playwright prints a ✘ for each
  # failing test and a "N failed" line in the footer.
  if grep -qE "✘[[:space:]].*rejected|failed|Error|AssertionError" "$log" 2>/dev/null; then
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
  exit 1
fi

echo "PARALLEL E2E: all groups passed"
