import { useCallback, useEffect, useState } from 'react'

import { api, projectPath } from '@/lib/api'

export type ObsEntry = {
  seq: number
  ts: string
  project: string
  trace: string
  source: string
  level: string
  type: string
  msg: string
  attrs?: Record<string, unknown>
}

export type ResourceSample = {
  ts: string
  cpuPercent: number
  memUsed: number
  memLimit: number
  netRx: number
  netTx: number
  blockR: number
  blockW: number
  pids: number
  diskUsed: number
  diskTotal: number
  state: string
}

export type ErrorGroup = {
  key: string
  type: string
  count: number
  firstSeen: string
  lastSeen: string
  sampleTrace: string
  sampleMsg: string
}

export type ObserveFilters = {
  level: string
  source: string
  type: string
  trace: string
  q: string
}

export const EMPTY_FILTERS: ObserveFilters = { level: 'all', source: 'all', type: '', trace: '', q: '' }

function qs(f: ObserveFilters): string {
  const p = new URLSearchParams()
  if (f.level && f.level !== 'all') p.set('level', f.level)
  if (f.source && f.source !== 'all') p.set('source', f.source)
  if (f.type) p.set('type', f.type)
  if (f.trace) p.set('trace', f.trace)
  if (f.q) p.set('q', f.q)
  p.set('limit', '200')
  return p.toString()
}

// useObserve tails one project's observe log with seq cursors, plus stats
// and the errors projection. Follow polls every 3s while visible.
export function useObserve(projectId: string, filters: ObserveFilters, follow: boolean) {
  const [logs, setLogs] = useState<ObsEntry[]>([])
  const [firstSeq, setFirstSeq] = useState(0)
  const [lastSeq, setLastSeq] = useState(0)
  const [stats, setStats] = useState<ResourceSample | null>(null)
  const [samples, setSamples] = useState<number[]>([])
  const [groups, setGroups] = useState<ErrorGroup[]>([])

  const key = `${filters.level}|${filters.source}|${filters.type}|${filters.trace}|${filters.q}`

  const refresh = useCallback(async () => {
    try {
      const d = await api<{ logs: ObsEntry[]; firstSeq: number; lastSeq: number }>(
        projectPath(projectId, `/observe?${qs(filters)}`),
      )
      setLogs((prev) => {
        // Follow appends newer; filter changes replace. Cap DOM at 1000, drop middle.
        const next = lastSeq > 0 && d.firstSeq > lastSeq ? [...prev, ...d.logs.filter((l) => l.seq > lastSeq)] : d.logs
        return next.length > 1000 ? [...next.slice(0, 100), ...next.slice(next.length - 900)] : next
      })
      setFirstSeq(d.firstSeq ?? 0)
      setLastSeq(d.lastSeq ?? 0)
    } catch {
      // keep last-known
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, key])

  const loadOlder = useCallback(async () => {
    if (!firstSeq || firstSeq <= 1) return
    try {
      const d = await api<{ logs: ObsEntry[]; firstSeq: number }>(
        projectPath(projectId, `/observe?${qs(filters)}&before=${firstSeq}`),
      )
      if (d.logs.length > 0) {
        setLogs((prev) => [...d.logs, ...prev].slice(-1000))
        setFirstSeq(d.firstSeq ?? firstSeq)
      }
    } catch {
      // keep last-known
    }
    // key carries the filters identity for the query string
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, key, firstSeq])

  useEffect(() => {
    setLogs([])
    setFirstSeq(0)
    setLastSeq(0)
    refresh()
  }, [projectId, key, refresh])

  useEffect(() => {
    if (!follow || document.hidden) return
    const id = setInterval(() => { if (!document.hidden) void refresh() }, 3000)
    return () => clearInterval(id)
  }, [follow, refresh])

  useEffect(() => {
    let stop = false
    const poll = async () => {
      try {
        const s = await api<ResourceSample>(projectPath(projectId, '/observe/stats'))
        if (stop) return
        setStats(s)
        setSamples((prev) => [...prev, s.cpuPercent ?? 0].slice(-60))
      } catch {
        // keep last-known
      }
    }
    poll()
    const id = setInterval(() => { if (!document.hidden) void poll() }, 3000)
    return () => { stop = true; clearInterval(id) }
  }, [projectId])

  useEffect(() => {
    let stop = false
    const poll = async () => {
      try {
        const d = await api<{ groups: ErrorGroup[] }>(projectPath(projectId, '/observe/errors'))
        if (!stop) setGroups(d.groups ?? [])
      } catch {
        // keep last-known
      }
    }
    poll()
    const id = setInterval(poll, 5000)
    return () => { stop = true; clearInterval(id) }
  }, [projectId])

  return { logs, firstSeq, lastSeq, refresh, loadOlder, follow, stats, samples, groups }
}
