import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, ChevronDown } from 'lucide-react'

import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import { api } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { EMPTY_FILTERS, useObserve, type ObserveFilters } from '@/hooks/useObserve'

const LEVELS = ['all', 'info', 'warn', 'error']
const SOURCES = ['all', 'server', 'preview', 'auth', 'system']
const PANELS = ['overview', 'runtime', 'build', 'errors'] as const
type Panel = (typeof PANELS)[number]

type ObserveMeta = { auditTypes: string[]; buildTypes: string[] }

function timeOfDay(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

function levelColor(level: string): string {
  return level === 'error' ? 'bg-red-500' : level === 'warn' ? 'bg-amber-400' : 'bg-emerald-500'
}

function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MB`
  return `${(n / 1024 ** 3).toFixed(1)} GB`
}

function Meter({ used, total, testid, label }: { used: number; total: number; testid: string; label: string }) {
  if (!(total > 0)) return null
  const pct = Math.min(100, Math.max(0, (used / total) * 100))
  return (
    <div className="flex flex-col gap-1" data-testid={testid}>
      <div className="flex items-baseline justify-between text-xs">
        <span className="text-muted-foreground">{label}</span>
        <span className="font-mono">{formatBytes(used)} / {formatBytes(total)} · {pct.toFixed(0)}%</span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-muted">
        <div className="h-full rounded-full bg-emerald-500/80" style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

// Nerdy Stuff: per-project observability replacing the Logs tab. One panel
// visible at a time (segmented control); md:+ adds a facet rail. Overview
// is the system metrics; Runtime is the unified tail with Follow ON (plus
// an audit toggle for the milestone subset); Build pins lifecycle lines
// while not ready.
export function NerdyStuffTab({ projectId }: { projectId: string }) {
  const [panel, setPanel] = useState<Panel>('runtime')
  const [filters, setFilters] = useState<ObserveFilters>(EMPTY_FILTERS)
  const [q, setQ] = useState('')
  const [follow, setFollow] = useState(true)
  const [expanded, setExpanded] = useState<number | null>(null)
  const [copied, setCopied] = useState(false)
  const [showFilters, setShowFilters] = useState(false)
  const bodyRef = useRef<HTMLDivElement>(null)
  // Milestone sets come from the backend (/api/observe/meta) so the UI can
  // never drift from the keys handlers actually emit.
  const [meta, setMeta] = useState<ObserveMeta | null>(null)
  useEffect(() => {
    api<ObserveMeta>('/api/observe/meta').then(setMeta).catch(() => {})
  }, [])
  const audit = filters.type !== ''

  // Search is debounced 200ms into the facet query.
  useEffect(() => {
    const id = setTimeout(() => setFilters((f) => ({ ...f, q })), 200)
    return () => clearTimeout(id)
  }, [q])

  const { logs, firstSeq, loadOlder, stats, samples, groups } = useObserve(projectId, filters, follow)

  // Mobile filter section auto-collapses on select (dropdown behavior):
  // on desktop the chips are always visible so this is a no-op there.
  function pickFilters(next: (f: ObserveFilters) => ObserveFilters) {
    setFilters(next)
    setShowFilters(false)
  }

  function toggleAudit() {
    if (!meta) return
    pickFilters((f) => ({ ...f, type: f.type === '' ? meta.auditTypes.join(',') : '' }))
  }

  // Non-default filter count, shown on the mobile Filters toggle.
  const activeFilters = (filters.level !== 'all' ? 1 : 0) + (filters.source !== 'all' ? 1 : 0) +
    (audit ? 1 : 0) + (q !== '' ? 1 : 0)

  function panelLabel(p: Panel): string {
    return p[0].toUpperCase() + p.slice(1) + (p === 'errors' && groups.length > 0 ? ` (${groups.length})` : '')
  }

  const seenTypes = useMemo(() => [...new Set(logs.map((l) => l.type))].sort(), [logs])
  const buildSet = useMemo(() => new Set(meta?.buildTypes ?? []), [meta])
  const buildLogs = useMemo(() => logs.filter((l) => buildSet.has(l.type)), [logs, buildSet])
  const ready = stats?.state === 'running'

  // Follow only while pinned to the bottom; reading history pauses it.
  useEffect(() => {
    const el = bodyRef.current
    if (!el || !follow) return
    el.scrollTop = el.scrollHeight
  }, [logs, follow])

  function onScroll() {
    const el = bodyRef.current
    if (!el) return
    const pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 48
    if (!pinned && follow) setFollow(false)
  }

  async function copyTrace(trace: string) {
    await copyToClipboard(trace)
    setCopied(true)
    setTimeout(() => setCopied(false), 1200)
  }

  function gotoTrace(trace: string) {
    setFilters((f) => ({ ...f, trace }))
    setPanel('runtime')
  }

  return (
    <Card className="flex h-full w-full flex-col shadow-sm" data-testid="nerdy-tab">
      <CardHeader className="pb-2">
        <div className="flex items-center gap-2">
          <CardTitle className="text-base">Nerdy Stuff</CardTitle>
          {/* Mobile: one dropdown instead of four tab buttons. Desktop keeps
              the segmented control. */}
          <div className="flex-1 md:hidden">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <Button variant="outline" size="sm" className="w-full justify-between" data-testid="nerdy-panel-select">
                  {panelLabel(panel)}
                  <ChevronDown className="size-3.5 shrink-0 text-muted-foreground" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-48">
                {PANELS.map((p) => (
                  <DropdownMenuItem key={p} data-testid={`nerdy-panel-opt-${p}`} onSelect={() => setPanel(p)}>
                    <span className="flex-1">{panelLabel(p)}</span>
                    {panel === p && <Check className="size-3.5" />}
                  </DropdownMenuItem>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </div>
        <CardDescription className="hidden md:block">Per-project observability — live metrics, logs, builds, errors.</CardDescription>
        <div className="hidden flex-wrap gap-1 md:flex" role="tablist" aria-label="Nerdy panels">
          {PANELS.map((p) => (
            <Button key={p} variant={panel === p ? 'default' : 'outline'} size="sm" role="tab" aria-selected={panel === p}
              data-testid={`nerdy-panel-${p}`} onClick={() => setPanel(p)}>
              {p[0].toUpperCase() + p.slice(1)}{p === 'errors' && groups.length > 0 ? ` (${groups.length})` : ''}
            </Button>
          ))}
        </div>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col gap-1 md:flex-row md:gap-2">
        {/* Facet rail: desktop only, deselect-to-zero allowed via checkboxes */}
        {(panel === 'runtime' || panel === 'build') && (
          <div className="hidden w-48 shrink-0 flex-col gap-3 overflow-auto md:flex" data-testid="nerdy-facets">
            <div>
              <p className="pb-1 text-xs font-medium">Level</p>
              {(['info', 'warn', 'error'] as const).map((l) => (
                <label key={l} className="flex items-center gap-2 text-xs">
                  <input type="checkbox" data-testid={`nerdy-facet-level-${l}`}
                    checked={filters.level === 'all' || filters.level === l}
                    onChange={() => setFilters((f) => ({ ...f, level: f.level === l ? 'all' : l }))} />
                  {l}
                </label>
              ))}
            </div>
            <div>
              <p className="pb-1 text-xs font-medium">Source</p>
              {(['server', 'preview', 'auth', 'system'] as const).map((s) => (
                <label key={s} className="flex items-center gap-2 text-xs">
                  <input type="checkbox" data-testid={`nerdy-facet-source-${s}`}
                    checked={filters.source === 'all' || filters.source === s}
                    onChange={() => setFilters((f) => ({ ...f, source: f.source === s ? 'all' : s }))} />
                  {s}
                </label>
              ))}
            </div>
          </div>
        )}

        <div className="flex min-h-0 flex-1 flex-col gap-1 md:gap-2">
          {panel === 'overview' && (
            <div className="flex flex-col gap-2" data-testid="nerdy-overview">
              <div className="grid grid-cols-2 gap-2">
                <div className="rounded-lg border bg-card p-3">
                  <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">CPU</p>
                  <p className="font-mono text-2xl leading-tight" data-testid="nerdy-cpu">
                    {(stats?.cpuPercent ?? 0).toFixed(1)}%
                  </p>
                  <div className="flex h-6 items-end gap-px pt-1" data-testid="nerdy-sparkline" aria-label="CPU sparkline">
                    {(samples.length ? samples : [0]).map((v, i) => (
                      <div key={i} className="w-1 rounded-sm bg-emerald-500/70" style={{ height: `${Math.min(100, Math.max(6, v))}%` }} />
                    ))}
                  </div>
                </div>
                <div className="rounded-lg border bg-card p-3">
                  <p className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">State</p>
                  <p className="pt-1"><Badge data-testid="nerdy-stats-state">{stats?.state ?? 'unknown'}</Badge></p>
                  <p className="pt-1 font-mono text-xs text-muted-foreground" data-testid="nerdy-pids">{stats?.pids ?? 0} pids</p>
                </div>
              </div>
              <div className="rounded-lg border bg-card p-3">
                <p className="pb-2 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Memory & disk</p>
                <div className="flex flex-col gap-2">
                  <Meter used={stats?.memUsed ?? 0} total={stats?.memLimit ?? 0} testid="nerdy-mem" label="memory" />
                  <Meter used={stats?.diskUsed ?? 0} total={stats?.diskTotal ?? 0} testid="nerdy-disk" label="disk" />
                </div>
              </div>
              {stats && (
                <div className="rounded-lg border bg-card p-3">
                  <p className="pb-1 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">Throughput</p>
                  <div className="grid grid-cols-2 gap-1 font-mono text-xs text-muted-foreground" data-testid="nerdy-io">
                    <span>net ↓ {formatBytes(stats.netRx)}</span>
                    <span>net ↑ {formatBytes(stats.netTx)}</span>
                    <span>blk read {formatBytes(stats.blockR)}</span>
                    <span>blk write {formatBytes(stats.blockW)}</span>
                  </div>
                </div>
              )}
              <p className="text-xs text-muted-foreground" data-testid="nerdy-health">
                health: {ready ? 'ready' : stats?.state ?? 'unknown'} · {logs.length} logs tailed · {groups.length} error groups
              </p>
              {!ready && <p className="text-xs">Build pinned while not ready — see the Build panel.</p>}
            </div>
          )}

          {(panel === 'runtime' || panel === 'build') && (
            <>
              {/* Mobile: chips collapse behind Filters so the log list keeps
                  the screen. Desktop shows them inline. */}
              <div className="md:hidden">
                <Button variant={activeFilters > 0 ? 'default' : 'outline'} size="sm"
                  data-testid="nerdy-filters-toggle" onClick={() => setShowFilters((f) => !f)}>
                  Filters{activeFilters > 0 ? ` (${activeFilters})` : ''}
                  <ChevronDown className={`size-3.5 transition-transform ${showFilters ? 'rotate-180' : ''}`} />
                </Button>
              </div>
              <div className={`${showFilters ? 'flex' : 'hidden'} flex-wrap items-center gap-1 md:flex`}>
                {LEVELS.map((l) => (
                  <Button key={l} variant={filters.level === l ? 'default' : 'outline'} size="sm"
                    data-testid={`nerdy-level-${l}`} onClick={() => pickFilters((f) => ({ ...f, level: f.level === l ? 'all' : l }))}>{l}</Button>
                ))}
                <span className="w-2" />
                {SOURCES.map((s) => (
                  <Button key={s} variant={filters.source === s ? 'default' : 'outline'} size="sm"
                    data-testid={`nerdy-source-${s}`} onClick={() => pickFilters((f) => ({ ...f, source: f.source === s ? 'all' : s }))}>{s}</Button>
                ))}
                {meta && meta.auditTypes.length > 0 && (
                  <>
                    <span className="w-2" />
                    <Button variant={audit ? 'default' : 'outline'} size="sm"
                      data-testid="nerdy-audit" onClick={toggleAudit}
                      title="Milestones only: created, launched, attached…">audit</Button>
                  </>
                )}
              </div>
              <div className="flex gap-2">
                <Input placeholder="Search msg, type, attrs…" value={q} onChange={(e) => setQ(e.target.value)}
                  data-testid="nerdy-search" list="nerdy-types" className="h-8 text-xs" />
                <datalist id="nerdy-types">
                  {seenTypes.map((t) => <option key={t} value={t} />)}
                </datalist>
                <Button variant={follow ? 'default' : 'outline'} size="sm" data-testid="nerdy-follow"
                  onClick={() => setFollow((f) => !f)}>
                  {follow ? 'Following' : 'Follow'}
                </Button>
              </div>
              {(panel === 'build' ? buildLogs : logs).length === 0 ? (
                <p className="text-xs text-muted-foreground" data-testid="nerdy-empty">
                  {panel === 'build'
                    ? 'No build history yet — lifecycle lines appear on create, start/stop, and recovery'
                    : audit
                      ? 'No milestones yet — create a session or start a preview'
                      : 'Nothing nerdy yet — create a project or start a preview'}
                </p>
              ) : (
                <div ref={bodyRef} onScroll={onScroll} data-testid="nerdy-list"
                  className="h-full min-h-0 overflow-auto rounded-lg bg-black p-3 font-mono text-xs text-zinc-200">
                  {firstSeq > 1 && (
                    <Button variant="ghost" size="sm" data-testid="nerdy-load-older" onClick={loadOlder}
                      className="mb-2 text-zinc-300">Load older</Button>
                  )}
                  {(panel === 'build' ? buildLogs : logs).map((l) => (
                    <div key={l.seq} data-testid={`nerdy-row-${l.seq}`}>
                      <div className="flex cursor-pointer gap-2 py-px" data-testid={`nerdy-row-toggle-${l.seq}`}
                        onClick={() => setExpanded((e) => (e === l.seq ? null : l.seq))}>
                        <span className="shrink-0 text-zinc-500">{timeOfDay(l.ts)}</span>
                        <span className={`mt-1 size-1.5 shrink-0 rounded-full ${levelColor(l.level)}`} />
                        <span className="break-all">{l.msg}</span>
                      </div>
                      {expanded === l.seq && (
                        <div className="ml-4 rounded bg-zinc-900 p-2" data-testid={`nerdy-expand-${l.seq}`}>
                          <p>type: {l.type} · source: {l.source}</p>
                          <p className="flex items-center gap-2">trace: {l.trace}
                            <Button variant="ghost" size="sm" data-testid="nerdy-copy-trace"
                              onClick={(e) => { e.stopPropagation(); copyTrace(l.trace) }}>
                              {copied ? 'Copied' : 'Copy'}
                            </Button>
                          </p>
                          {l.attrs && <pre className="whitespace-pre-wrap break-all">{JSON.stringify(l.attrs, null, 2)}</pre>}
                          {!audit && <Button variant="ghost" size="sm" onClick={() => { if (!audit) toggleAudit() }}>Milestones only</Button>}
                        </div>
                      )}
                    </div>
                  ))}
                </div>
              )}
              {!follow && (
                <Button size="sm" className="sticky bottom-0" data-testid="nerdy-resume"
                  onClick={() => setFollow(true)}>Resume follow</Button>
              )}
            </>
          )}

          {panel === 'errors' && (
            <div className="overflow-auto" data-testid="nerdy-errors">
              {groups.length === 0 ? (
                <p className="text-xs text-muted-foreground">No errors grouped yet.</p>
              ) : groups.map((g) => (
                <div key={g.key} data-testid={`nerdy-error-${g.type}`}
                  className="flex cursor-pointer items-center gap-2 py-1 text-xs"
                  onClick={() => gotoTrace(g.sampleTrace)}>
                  <Badge variant="destructive">{g.count}</Badge>
                  <span className="break-all">{g.sampleMsg}</span>
                </div>
              ))}
            </div>
          )}

        </div>
      </CardContent>
    </Card>
  )
}
