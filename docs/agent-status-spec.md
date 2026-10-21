# Harness activity notifications

## Feature

Notify the user when a CLI launched through a registered harness has gone
quiet long enough that it may be ready for the user to return. This is a
prompt to check the terminal, not a claim that the task succeeded or that the
CLI is waiting for input.

Only launches made through the harness modal are in scope. Do not inspect
arbitrary terminal text to discover manually started CLIs, and do not infer a
harness from command strings typed into a shell.

## User journey

1. The user launches a registered harness from the harness modal.
2. That launch enrolls the specific tmux session/pane and harness instance for
   monitoring.
3. While the CLI produces terminal output, its quiet timer is reset.
4. If output stays quiet for the configured interval, notify the user once:
   the harness may need attention. This is done via email. 
5. If the CLI later produces output, clear the notified state and start a new
   quiet interval. A later quiet period can notify again.
6. When the CLI exits, its pane/session closes, or the terminal is replaced,
   remove that instance from monitoring. Do not notify for it afterward.

## Technology

- The harness launch path already knows the harness ID and the tmux session it
  starts. Use that authoritative launch event to enroll; no command scanning
  or CLI-specific hooks are needed.
- Use tmux `pipe-pane` to observe pane output as it happens and reset the quiet
  timer. This avoids repeatedly capturing or hashing the screen. Keep one
  monitor per enrolled pane, and stop it when that pane is invalidated.
- Tie the monitor to the launched CLI lifecycle, not merely to the tmux
  session. The harness command currently runs inside a shell wrapper, so the
  implementation must explicitly report CLI exit before the wrapper returns
  to a shell; the still-open pane must not keep the CLI enrolled.
- Persist the quiet interval as a single sensible default/configuration point.
  The initial value is a product choice; it should be long enough to avoid
  noisy notifications during normal pauses.
- Deliver notifications through the app's existing notification mechanism.
  This spec does not prescribe a new notification transport.

`pipe-pane` observes output, not semantic progress or success. A quiet CLI can
be done, waiting for input, blocked, or simply slow. The notification wording
must preserve that uncertainty.

## Enrollment and invalidation

Enroll only after a successful harness-modal launch, using a stable identity
for that exact harness instance (session plus pane or an equivalent unique
launch ID). Store the harness ID and enough lifecycle identity to reject stale
events. Re-launching a harness creates a distinct instance; it must not
inherit a prior instance's timer or notified state.

Reset the quiet timer on observed pane output. If output arrives after a
notification, mark the instance unnotified and begin a fresh quiet interval.
Do not treat the notification itself as completion or remove the instance.

Invalidate and stop monitoring when any of these occurs:

- the launched CLI exits, regardless of exit code;
- its tmux pane or session is closed or killed;
- the session/pane is replaced or restarted, making the enrolled identity
  stale;
- monitoring setup fails or the app can no longer establish that the same
  launched instance is alive.

Ctrl-C, Ctrl-D, or another keypress alone is not proof of exit: the CLI may
handle it and continue. Use observed CLI lifecycle/exit and tmux pane/session
events to invalidate, not key names. The harness wrapper's exit marker is one
possible lifecycle signal, but it must be associated with the enrolled
instance and processed before subsequent shell output can be mistaken for CLI
activity.

## Invariants

- **Harness-only enrollment:** manually started commands and ordinary shell
  activity never enter the watchlist.
- **One live record per instance:** repeated launch/activity events cannot
  create duplicate monitors or duplicate notifications for the same quiet
  period.
- **Instance isolation:** output, exit, and timer events affect only the
  matching harness instance, never a later process that reused a session or
  pane name.
- **Quiet means no observed output:** elapsed time is measured since the last
  pane output (or enrollment if no output has occurred). This is a heuristic,
  not a completion detector.
- **At most one notification per quiet period:** after notifying, remain
  notified until new output is observed; timer ticks alone never notify again.
- **Activity rearms:** output after notification clears the notified state and
  starts a new full quiet interval.
- **Exit wins:** once invalidated, queued timers or late output cannot notify
  that instance.
- **No false success claim:** notification means “check this harness”; it does
  not mean task completed, succeeded, or awaits input.
- **Bounded resources:** every enrollment owns at most one output pipe and one
  timer, both cleaned up on invalidation.
- **Restart safety:** stale monitoring state after app/server restart must not
  generate notifications for sessions whose live identity/lifecycle cannot be
  revalidated.

## Out of scope

- Detecting commands typed or pasted into ordinary terminals.
- Recognizing whether arbitrary processes are AI CLIs.
- Per-harness prompt parsing, hooks, or success detection.
- Claiming guaranteed task completion. The goal is to signal the user to
  return and interact when a managed CLI has gone quiet.

## Verification expectations

Tests should cover harness-modal enrollment, non-enrollment of ordinary
sessions, output resetting the timer, one notification per quiet period,
re-arming after post-notification output, CLI exit before the timeout, pane or
session closure, stale events after restart/reuse, and cleanup of the output
pipe and timer. Include a long-running CLI that becomes quiet and confirm the
user receives one “may need attention” notification without asserting task
success.

E2E tests can mock the tmux api as long as we mock with high fidelity. Then email can also be mocked by just a counter to assert it was called. On top of that, the e2e tests should be realistic tho, so we'll actually do like a short wait of 1/2s of no activity. 