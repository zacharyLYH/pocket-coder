# Agent Pipeline Spec

Status: implemented. This is the shape the code now has.

## The problem in one sentence

A "turn" is the same object in butler and codemap, but nowhere in the codebase
is there a thing called a turn — its shape was emergent from two ~100-line
handlers, each with closures mutating captured locals, plus two near-copies of
history rebuilding, plus two hand-rolled event-collecting state machines.

## The mental model after

An agent turn is a pipeline of five stages. Data moves left to right; nothing
branches outside a stage:

```
scope? → context → tools → run → post
```

- **scope** — optional cheap gate before the loop. Zero or one stage call.
  Butler's `can_help` classifier and codemap's `about_code` classifier are
  both this — same `ScopeGate` struct, different prompt and verdict. A gate
  only ever refutes: any gate failure falls through to the loop.
- **context** — pure function: persisted turns + new prompt → conversation.
  One shared implementation, parameterized by caps. Butler and codemap each
  contribute only a mapper from their persisted turn type to a common view.
- **tools** — the registry for this turn: reads, writes, schemas, enums.
  Purely declarative per caller; the engine never knows what a tool means.
- **run** — the engine. Consumes the assembled turn, produces the transcript.
  Already exists (`agent.Run`); it just stops being buried in handler closures.
- **post** — caller-owned shaping of the output. Deliberately bespoke:
  codemap hydrates and shapes sections, butler normalizes confirm cards.
  This is domain logic, not shared machinery, and stays that way.

Two identities hold everywhere:

1. **The engine owns mechanics; the caller owns policy.** Anything about *how*
   a loop executes (step budgets, grounding, repeat notes, transcript capture)
   lives in `agent`. Anything about *what* butler or codemap is (guides, tool
   sets, scope rules, output shape) lives with butler or codemap.
2. **Every stage reads like a pure function**, loosely: data in, data out,
   side effects only where the stage's name says so (`run` streams, `post`
   persists).

## What the final code feels like

Reading a butler turn should take one screen and go top-to-bottom in execution
order — the same five nouns, in the same order, as codemap:

```go
out := agent.Pipeline{
    Scope:   butlerScopeGate,        // refuse out-of-scope, one structured call
    Context: butlerContext(st),      // shared history builder, butler's mapper
    Tools:   butlerTools(d, st),     // reads + enumed propose-only writes
    Run:     agent.TurnSpec{...},    // guide, budget, streaming shape
    Post:    butlerPost,             // cards, steps, thread persistence
}.Execute(ctx, prompt)
```

The codemap version is the same five lines with different nouns. Anyone who
has read one turn can read the other. The interesting part of each caller is
only its policy, and the policy is data.

## The transcript shape exists once

The loop emits events; exactly one small collector in `agent` folds events
into a `[]Round` transcript (thought + tool steps + errors). Butler and codemap
each keep one dumb mapper from `[]Round` to their persisted shape. The two
hand-rolled per-caller event state machines — the buggiest duplicated surface
today — are deleted, not shared.

## Complexity budget

- Net lines: **negative**. ~200 lines of duplicated machinery deleted; the
  shared pieces added are smaller than what they replace.
- No generics, no `Stage[I, O]` combinators, no interface hierarchies. A
  pipeline in Go that needs type-level plumbing is the same unreadability in a
  nicer coat. Plain structs and functions only.
- The whole engine stays stdlib + the one OpenAI SDK, as today.
- If a stage can't be explained in one sentence without "and also", it is two
  stages.

## Explicitly out of scope

- No behavior change to butler: same tools, guides, prompts, SSE events,
  persisted files, confirm flow.
- Codemap gains one deliberate behavior change: the `about_code` scope gate
  (its mirror of butler's `can_help`), which the old code did only in prose.
- No mode system, no phase machine, no round enforcement (decided: dropped).
- Codemap's hydrate/shape and its multi-round history replay (thoughts +
  section transcripts) stay bespoke. Shared ≠ uniform.

## Observability note

The engine reports through one callback (`OnTrace`); lineage rides the
`TurnResult`. Each caller maps that stream onto its own observer (butler: SSE
status lines; codemap: structured obs logging with durations). Unifying
obs *storage* behind an interface is possible later, but the two callers log
different shapes to different sinks — forcing one interface now would be
type plumbing, not simplification.

## Sequencing (as shipped)

`agent/turn.go` landed first (Pipeline, RoundCollector, BuildHistory,
ScopeGate); butler migrated, then codemap. Codemap's bespoke history replay
was kept after inspection: it replays per-round thoughts and section
transcripts, which is codemap's own transcript format, not duplication of
butler's.
