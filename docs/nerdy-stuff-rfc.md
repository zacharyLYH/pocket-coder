# RFC: `Nerdy Stuff` — Per-Project Observability (replaces Logs tab)

Status: DRAFT — plan only, no code yet.
Replaces: Logs tab (`tab-logs` → `tab-nerdy`).
Scope: A (platform) only. No B (tmux pane output), No C (app stdout).

## 1. Problem

Today we have 4 log surfaces with no e2e story:

1. `slog` text to stderr — boot, http, ad-hoc. Dev-only, not queryable.
2. `events.log` (`$DATA_DIR/events.log`, append-only JSONL) — global audit, but no per-project slice, no levels.
3. `projectlog` ring (in-mem, 500 entries/project, dies on restart) — what Logs tab tails. No persistence, no search.
4. `docker logs` — effectively empty forever (PID1 = `sleep infinity`). Real user output lives in tmux panes, which we deliberately do NOT scope in.

Result: confusing, no persistence across restart, no levels/facets, no resources, no errors inbox, no correlation (`create -> image.build -> ready` interleaved).

PRD §12 already wants: live state + usage + log tails + errors + auth + health. This RFC pins how.

## 2. Goals / Non-goals

Goals: per-project live resources (CPU+mem+disk+net+state); one parseable platform log per project, persisted, capped, searchable; typed `obs` logger with OTel-lite trace; grouped errors inbox; Activity timeline; Health card; Sessions meta; mobile-first `Nerdy Stuff` UI; faked-data e2e with mobile+desktop screenshots.

Non-goals: B/C (tmux tail, app stdout, docker tailer, Vector, Prometheus, OTel SDK); full query language; infinite scroll (use `Load older` button); `server.log.jsonl` (dropped — `events.log` IS the server log).

## 3. IA: `Nerdy Stuff`

Replaces `LogsTab` mount in `TerminalView`. `TerminalTabs.fixed` entry `{id:'nerdy', label:'Nerdy Stuff', testid:'tab-nerdy'}`.

Overview (status+sparkline+health) / Runtime (unified tail, Follow ON) / Build (type in create,image.build,clone,ready,reconcile; pinned while not ready) / Activity (events.log filtered data.id==project) / Errors (grouped) / Sessions (meta only, no output).

Mobile: segmented control, one panel visible, Follow sticky bottom. Desktop (md:+): left facet rail with checkboxes (deselect-to-zero allowed), search debounced 200ms, row expand -> JSON + Copy trace.

## 4. Data structures

Envelope (one JSON shape everywhere):

```json
{"ts":"2026-09-17T05:00:00.000000000Z","seq":42,"project":"owner/repo","trace":"9f3a2c1d4e5f6a7b","source":"server","level":"info","type":"project.ready","msg":"clone landed","attrs":{"sha":"abc123"}}
```

`seq` per-file monotonic (atomic.Int64), IS the pagination cursor. `trace` hex16 per request in ctx, hidden in UI by default. `source` enum server|preview|auth|system. `type` stable key, `msg` human, `attrs` from slog.Attr single-line JSON. Levels info|warn|error only.

Files:

```
$DATA_DIR/
  events.log                       # existing, global, append-only, never rotated
  observe/<escaped>.log.jsonl      # NEW per-project slice, 4MB active + 1x .1 backup
  observe/<escaped>.log.jsonl.1
```


`escaped` = SanitizeName. Delete project -> RemoveAll. Project lines go to both files.

## 5. Algorithms

### 5.1 Buffered writer, inline purge, no daemon, no lock

Single flush-loop writer owns the file. Append() is non-blocking chan send (1024 buffer, drop + count on full). Loop ticks 1s: drain -> bufio write -> flush/fsync -> if size>4MB rotate (close, rename cur->.1 unlinking old .1, create new). seq via atomic.Int64, size owned by loop only. Readers open read-only seek-from-end, never lock.

### 5.2 Read: seek-from-end, <=200 lines per request

GET initial limit=200 scans back from EOF to N newlines, parses, filters level/source/q/type/trace, returns {logs, firstSeq, lastSeq}. ?after=lastSeq forward tail (Follow 3s poll). ?before=firstSeq&limit=200 older chunk (Load older button). Never full 4MB scan. DOM cap 1000, drop middle.

### 5.3 Stats

docker.Client += StatsOneShot + DiskUsage (exec df -k /workspace). CPU% = (cpuDelta/systemDelta)*onlineCPUs*100 with ~2s server-side precpu cache. GET observe/stats every 3s while Overview mounted + document.visible. Disk every poll vs 30s cache: OPEN.

### 5.4 Errors grouping

Projection only. Scan observe+events project slice level==error, key = type + template(msg) stripping hex/sha/numbers/IPs. Row {key,count,firstSeen,lastSeen,sampleTrace,sampleMsg}, sort count desc, top 20, 5s cache. Click -> Runtime trace=sampleTrace.

### 5.5 Activity

events.log filtered data.id==projectID -> human verbs. Reuses GET /api/events, no index v1.

### 5.6 ResourceSample

```go
type ResourceSample struct {
  TS time.Time
  CPUPercent float64
  MemUsed, MemLimit uint64
  NetRX, NetTX uint64
  BlockR, BlockW uint64
  PIDs int
  DiskUsed, DiskTotal uint64
  State string // running|exited|missing
}
```

No persistence. FE keeps last 60 in-memory for sparkline.

## 6. Backend API

- Middleware mints trace hex16 -> ctx (obs.WithTrace). internal/obs Info/Warn/Error(ctx, projectID, typ, msg, attrs...slog.Attr) writes events.Append + observe file (if projectID) + slog stderr.
- GET /api/projects/:id/observe?after&before&limit(def 200, max 1000)&level&source&type&trace&q -> {logs, firstSeq, lastSeq}
- GET /api/projects/:id/observe/stats -> ResourceSample
- GET /api/projects/:id/observe/errors -> {groups} (projection, 5s cache)
- GET /api/events (existing) for Activity.
- Compat: keep GET/POST /logs as shim one release, then delete projectlog + plog().

## 7. UI details

Empty state: "Nothing nerdy yet — create a project or start a preview". Level chips all|info|warn|error; source chips server|preview|auth|system; type datalist from seen types. Search free-text over msg+type+attrs, case-insensitive, 200ms debounce. Row: time|level dot|msg. Expand: type, source, trace [Copy], attrs pretty JSON, View in Activity. Follow ON default, auto-off on scroll-up, sticky resume. Load older top button hidden when firstSeq<=1. Sparkline div bars/SVG, no chart dep. shadcn Card/Badge/Input/Checkbox/Button/ScrollArea.

## 8. Testing

Go unit: envelope single-line JSON, template() stripping, 4MB rotation, seek-back reader, CPU delta, trace middleware. Playwright route-mocked (no engine): fixture observe.json (20 mixed + 1 error group) + stats.json + activity.json; web/e2e/nerdy.mobile.spec.ts (390x844) + nerdy.desktop.spec.ts (1280x720): tail -> chip filter -> expand -> copy trace -> Load older -> Follow pause/resume -> toHaveScreenshot per variant. Go integration: create project -> observe file has project.create/ready sharing one trace.

## 9. Open decisions

1. Backup count 1x .1 (8MB worst/proj) OK?
2. Trace hidden-by-default + Copy vs ?debug=1 only?
3. Disk df every 3s vs 30s cache?
4. Build separate tab vs pinned section while not ready?
5. Delete projectlog + LogsTab same PR or shim first?

## 10. Build order

1. internal/obs + writer + rotation + trace middleware.
2. Wire events + observe/*.log, migrate plog callers.
3. GET observe + stats + StatsOneShot/DiskUsage.
4. Errors + Activity projections.
5. FE NerdyStuffTab (Overview+Runtime first, then Build/Activity/Errors/Sessions-meta), delete LogsTab, mobile+desktop specs.

