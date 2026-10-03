# Preview relay unreachable from a containerized server on Docker Desktop

## Gory detail (paste this at an AI when asking about preview failures)

**Symptom.** On a Mac, any flow that opens a preview fails: the preview/app/stack
e2e snapshot specs cannot regenerate, and `preview_start` errors after a ~120s
hang. Linux CI is green for the same specs.

**Topology (four netns, three hosts).** The e2e server itself runs containerized
(`docker compose up --build server`, `web/playwright.config.ts` webServers) on
the user-defined bridge `pcoder-net`. A preview launch
(`server/internal/preview/docker_runtime.go:Start`) runs one browser sidecar
with `Network: container:<project-container>` (`docker.Spec`, `server/internal/docker/spec.go`),
so the sidecar shares the *project* container's network namespace and reaches
localhost-bound dev servers. The first endpoint tried is always the direct one:
`http://<project-bridge-IP>:9223` (the socat CDP proxy inside the sidecar).

**Why Linux works.** Server and project containers share `pcoder-net`, where
bridge IPs route container-to-container, containerized or not. `directCDPReady`
gets HTTP 200, the relay never starts, and the `127.0.0.1` code below never runs.
That is why CI never caught this.

**Why macOS takes the relay path.** On Docker Desktop, bridge IPs are not
dialable, so the direct probe fails and `Start` falls back to `startRelay`: a
second container on `pcoder-net` running
`socat TCP-LISTEN:<port>,fork TCP:<sidecar-IP>:<port>` for CDP (9223) and noVNC
(6080), with both ports published on the *Mac host's* loopback via
`PublishLoopback` (engine-assigned host ports, read back from `Inspect`).

**The bug.** `startRelay` built the endpoint as
`http://127.0.0.1:<published-port>`. That string is correct only when the
*server process* runs on the Mac host. But the server runs in a container, so
`127.0.0.1` resolves to the server container's own loopback, where nothing
listens. `WaitForCDP` then polls a dead address until `cdpWaitTimeout` (120s)
expires and `Start` returns an error. Every preview open on a Mac-backed server
dies here; nothing is wrong with Chromium, the sidecar, or the relay itself.

**The fix (relay by name, no new moving parts).** Server and relay already share
`pcoder-net`, a user-defined bridge, which gives embedded DNS: every container
on it resolves every other by name. So the endpoint is now
`http://<relay-container-name>:<internal-port>` (`pcoder-preview-relay-<id>`,
ports 9223/6080, no published-port lookup). Concretely, in `startRelay`:
socat listeners got explicit `bind=0.0.0.0` (matching `server/internal/preview/image/start-browser`),
and the returned `Endpoint` uses `spec.Name` plus the internal ports. The
loopback publications and the `Published` readiness check stay untouched: still
useful for host-side debugging, still asserting the relay fully started. Net
diff: 12 lines across `docker_runtime.go` and `docker_runtime_test.go` (relay
URL constants and the fake CDP client's answered host went from
`127.0.0.1:<port>` to `<relay-name>:<internal-port>`).

**How to verify.** `go test ./internal/preview/` (unit, mocked engine), then on
a Mac regenerate a preview group (`UPDATE_SNAPSHOTS=1 sh e2e-parallel.sh
preview.d`). The server log should show `preview phase: start relay` with a
`cdp` URL containing the relay container name, followed by `preview started`
instead of the 120s `wait for CDP` error.

---

## Plain version (how to think about it)

Your server runs *inside a container*, so when it says `localhost` it means
itself, not your Mac. It was asking itself for the preview browser, and nobody
there answered.

Questions that would have found this fast:
- "Where does each piece actually run: my laptop, or inside a container?"
- "When the code dials this address, whose network is it dialing from?"
- "This works on Linux but not Mac, so what differs about the network path,
  not the application logic?"
