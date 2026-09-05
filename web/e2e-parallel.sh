#!/bin/sh
# Run the e2e suite as PARALLEL PROCESSES, one per test type. Each process
# gets its own backend port, frontend port, and test-results subdirectory
# (E2E_API_PORT / E2E_WEB_PORT / E2E_RUN_ID — see e2e/env.ts), so groups
# share nothing: separate state.json, separate PIN log, separate auth state.
#
# Groups start a few seconds apart so their first project creations don't
# race each other's one-time project-image build on a cold engine.
#
# Usage:
#   sh e2e-parallel.sh                 # all four groups in parallel
#   sh e2e-parallel.sh stack sessions  # only the named groups
set -u
cd "$(dirname "$0")"

mkdir -p test-results

# name|offset|specs... — offsets keep every group's ports distinct from the
# others AND from a concurrently running default `npm run test:e2e`.
ALL_GROUPS='
app|1|e2e/app.spec.ts e2e/preview.basic.spec.ts e2e/preview.hmr.spec.ts e2e/sshkeys.e2e.spec.ts
stack|2|e2e/terminal.stack.spec.ts
sessions|3|e2e/terminal.session.spec.ts e2e/harness.inject.spec.ts e2e/harness.orchestration.spec.ts e2e/harness.relaunch.spec.ts
visual|4|e2e/home.visual.spec.ts
preview|5|e2e/preview.tools.spec.ts e2e/preview.auth.spec.ts e2e/preview.reconnect.spec.ts e2e/preview.journey.spec.ts e2e/quickcommands.spec.ts e2e/preview.htmx.spec.ts e2e/preview.vue.spec.ts e2e/preview.vanilla.spec.ts e2e/preview.viewport.spec.ts e2e/preview.fit.spec.ts
'

run_group() {
  name="$1"; offset="$2"; specs="$3"
  api=$((8080 + offset * 10))
  web=$((5170 + offset * 10))
  # Clean up any leftover containers from crashed runs
  docker compose -f ../docker-compose.e2e.yml -p "sps-e2e-$name" down 2>/dev/null
  echo "[$name] starting: api:$api web:$web → test-results/$name.log"
  E2E_RUN_ID="$name" E2E_API_PORT="$api" E2E_WEB_PORT="$web" \
    npx playwright test --config=playwright.config.ts $specs > "test-results/$name.log" 2>&1 &
  # remember pids via jobs; simplest is to wait on all and check logs below
}

WANTED="${*:-}"
pids=""
while IFS='|' read -r name offset specs; do
  [ -z "$name" ] && continue
  case " $WANTED " in
    *" $name "*|"  ") ;;
    *) continue ;;
  esac
  run_group "$name" "$offset" "$specs"
  pids="$pids $!"
  sleep 4 # stagger image-build / git-daemon startup races
done <<EOF
$(printf '%s\n' "$ALL_GROUPS")
EOF

fail=0
for p in $pids; do
  wait "$p" || fail=1
done

echo
for name in app stack sessions visual preview; do
  case " $WANTED " in *" $name "*|"  ") ;; *) continue ;; esac
  tail -n 3 "test-results/$name.log" | sed "s/^/[$name] /"
done

if [ "$fail" -ne 0 ]; then
  echo "PARALLEL E2E: FAILED (see test-results/<group>.log)"
  exit 1
fi

# Final cleanup of all groups
for name in app stack sessions visual preview; do
  docker compose -f ../docker-compose.e2e.yml -p "sps-e2e-$name" down 2>/dev/null
done

echo "PARALLEL E2E: all groups passed"
