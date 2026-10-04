import { useCallback, useEffect, useRef, useState } from 'react'

import { errMsg } from '@/lib/api'
import type { ThreadStatus } from '@/lib/types'

export function isUnsettled(status?: string): boolean {
  return status === 'running' || status === 'awaiting'
}

export function usePollWhileUnsettled(status: string | undefined, inFlight: boolean, poll: () => void): void {
  useEffect(() => {
    if (inFlight || !isUnsettled(status)) return
    const t = setInterval(poll, 3000)
    return () => clearInterval(t)
  }, [status, inFlight, poll])
}

export type ThreadOps<
  TThread extends { id: string; status?: ThreadStatus },
  TSummary extends { id: string; status?: ThreadStatus },
> = {
  list: () => Promise<{ threads: TSummary[] }>
  open: (id: string) => Promise<TThread>
  send: (prompt: string, threadId?: string) => Promise<{ threadId: string }>
  retry: (threadId: string) => Promise<{ threadId: string }>
  remove: (id: string) => Promise<void>
  autoOpen?: boolean
}

// useThread owns one server-side thread plus its list, shared by Butler
// and Codemap. The server is the source of truth: sending posts and then
// adopts the persisted thread, polling covers everyone else (including a
// reloaded starter). Draft text and overlays stay in the sheets.
export function useThread<
  TThread extends { id: string; status?: ThreadStatus },
  TSummary extends { id: string; status?: ThreadStatus },
>(ops: ThreadOps<TThread, TSummary>, resetKey = '') {
  const [threads, setThreads] = useState<TSummary[]>([])
  const [thread, setThread] = useState<TThread | null>(null)
  const [inFlight, setInFlight] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const opsRef = useRef(ops)
  opsRef.current = ops
  const threadRef = useRef<TThread | null>(null)
  threadRef.current = thread
  const inFlightRef = useRef(false)

  const refreshList = useCallback(async () => {
    try {
      const d = await opsRef.current.list()
      setThreads(d.threads ?? [])
      return d
    } catch {
      return null
    }
  }, [])

  const openThread = useCallback(async (id: string) => {
    setError(null)
    try {
      setThread(await opsRef.current.open(id))
    } catch (e) {
      setError(errMsg(e))
    }
  }, [])

  // Mount: list always; auto-open the running thread (remount-into-run)
  // else the newest one.
  useEffect(() => {
    setThread(null)
    setError(null)
    if (!opsRef.current.autoOpen) {
      void refreshList()
      return
    }
    let cancelled = false
    void (async () => {
      const loaded = await refreshList()
      if (cancelled || !loaded) return
      const target = loaded.threads.find((t) => t.status === 'running') ?? loaded.threads[0]
      if (target) void openThread(target.id)
    })()
    return () => {
      cancelled = true
    }
  }, [resetKey, refreshList, openThread])

  const poll = useCallback(() => {
    const id = threadRef.current?.id
    if (!id) return
    void openThread(id)
    void refreshList()
  }, [openThread, refreshList])
  usePollWhileUnsettled(thread?.status, inFlight, poll)

  // Adopt the persisted result unless the user navigated away mid-run;
  // then the list refresh is enough and the visible transcript is theirs.
  async function adopt(sendThreadId: string | undefined, threadId: string) {
    await refreshList()
    const current = threadRef.current
    if (sendThreadId == null ? current == null : current?.id === sendThreadId) {
      await openThread(threadId)
    }
  }

  const sendTurn = useCallback(async (prompt: string, threadId?: string) => {
    const p = prompt.trim()
    if (!p) return
    inFlightRef.current = true
    setInFlight(true)
    setError(null)
    try {
      const r = await opsRef.current.send(p, threadId)
      await adopt(threadId, r.threadId)
    } catch (e) {
      setError(errMsg(e))
      const tid = (e as { body?: { threadId?: string } }).body?.threadId ?? threadId
      if (tid) {
        const current = threadRef.current
        if (threadId == null ? current == null : current?.id === threadId) {
          try {
            setThread(await opsRef.current.open(tid))
          } catch {
            // Error banner already set; a stale transcript beats none.
          }
        }
      }
      void refreshList()
    } finally {
      inFlightRef.current = false
      setInFlight(false)
    }
  }, [refreshList, openThread])

  const retryTurn = useCallback(async (threadId: string) => {
    inFlightRef.current = true
    setInFlight(true)
    setError(null)
    try {
      const r = await opsRef.current.retry(threadId)
      await adopt(threadId, r.threadId)
    } catch (e) {
      setError(errMsg(e))
      if (threadRef.current?.id === threadId) {
        await openThread(threadId)
      }
      void refreshList()
    } finally {
      inFlightRef.current = false
      setInFlight(false)
    }
  }, [refreshList, openThread])

  function newChat() {
    setThread(null)
    setError(null)
  }

  const removeThread = useCallback(async (id: string) => {
    if (inFlightRef.current) return
    try {
      await opsRef.current.remove(id)
      setThreads((prev) => prev.filter((t) => t.id !== id))
      setThread((t) => (t?.id === id ? null : t))
    } catch (e) {
      setError(errMsg(e))
    }
  }, [])

  return { threads, thread, inFlight, error, setError, openThread, newChat, removeThread, sendTurn, retryTurn }
}
