import { useCallback, useEffect, useRef, useState } from 'react'
import { Bot, Plus, Send, Trash2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Avatar } from '@/components/ui/avatar'
import { ButlerSteps } from '@/components/ButlerSteps'
import { api, errMsg } from '@/lib/api'
import { postButlerTurn, type ButlerStatus } from '@/lib/butler'
import type { ButlerThread, ButlerThreadSummary, ButlerTurn } from '@/lib/types'

const PRESETS = [
  { label: 'Brief me', prompt: 'Brief me on my projects: what needs attention?' },
  { label: 'Wire my key', prompt: 'Help me wire up my git key.' },
  { label: 'New shortcut', prompt: 'Help me make a new shortcut.' },
]

export function ButlerSheet({ projectHint, onClearHint, onClose }: {
  projectHint: string | null
  onClearHint: () => void
  onClose: () => void
}) {
  const [threads, setThreads] = useState<ButlerThreadSummary[]>([])
  const [thread, setThread] = useState<ButlerThread | null>(null)
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const [statuses, setStatuses] = useState<ButlerStatus[]>([])
  const [error, setError] = useState<string | null>(null)
  const bottomRef = useRef<HTMLDivElement>(null)

  const refreshList = useCallback(async () => {
    try {
      const d = await api<{ threads: ButlerThreadSummary[] }>('/api/butler/threads')
      setThreads(d.threads ?? [])
    } catch { /* list failure must not break the composer */ }
  }, [])

  useEffect(() => { void refreshList() }, [refreshList])
  useEffect(() => {
    try { bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' }) } catch { /* jsdom has no layout */ }
  }, [thread?.turns.length, busy])

  async function openThread(id: string) {
    setError(null)
    try {
      const d = await api<{ thread: ButlerThread }>(`/api/butler/threads/${encodeURIComponent(id)}`)
      setThread(d.thread)
    } catch (e) { setError(errMsg(e)) }
  }

  function newChat() { setThread(null); setError(null); setStatuses([]) }

  async function del(id: string) {
    try {
      await api(`/api/butler/threads/${encodeURIComponent(id)}`, { method: 'DELETE' })
      setThreads((t) => t.filter((x) => x.id !== id))
      if (thread?.id === id) setThread(null)
    } catch (e) { setError(errMsg(e)) }
  }

  async function send(text: string) {
    const p = text.trim()
    if (!p || busy) return
    setBusy(true); setError(null); setStatuses([])
    const optimistic: ButlerTurn = { turnId: 'pending', prompt: p, projectHint: projectHint ?? undefined }
    const curId = thread?.id && thread.id !== 'new' ? thread.id : undefined
    setThread((t) => t ? { ...t, turns: [...t.turns, optimistic] } : { id: 'new', title: 'New chat', createdAt: '', updatedAt: '', turns: [optimistic] })
    setPrompt('')
    try {
      const r = await postButlerTurn({ prompt: p, threadId: curId, projectHint: projectHint ?? undefined }, (s) => setStatuses((prev) => [...prev, s]))
      const d = await api<{ thread: ButlerThread }>(`/api/butler/threads/${encodeURIComponent(r.threadId)}`)
      setThread(d.thread)
      void refreshList()
    } catch (e) {
      setError(errMsg(e))
      if (curId) void openThread(curId)
      else setThread(null)
      void refreshList()
    } finally { setBusy(false) }
  }

  const turns = thread?.turns ?? []
  const lastStatus = statuses.length > 0 ? statuses[statuses.length - 1] : null

  return (
    <div className="flex h-full flex-col" data-testid="butler-sheet">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <Avatar className="size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
        <h2 className="text-[15px] font-semibold tracking-tight">Butler</h2>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={newChat} data-testid="butler-new" aria-label="New chat"><Plus className="size-4" /></Button>
        <Button variant="ghost" size="sm" onClick={onClose} data-testid="butler-close" aria-label="Close"><X className="size-4" /></Button>
      </div>
      {threads.length > 0 && (
        <div className="flex gap-1.5 overflow-x-auto border-b px-3 py-2" data-testid="butler-threads">
          {threads.map((t) => (
            <div key={t.id} className="flex shrink-0 items-center gap-1">
              <Button variant={thread?.id === t.id ? 'secondary' : 'ghost'} size="sm" onClick={() => void openThread(t.id)} data-testid={`butler-thread-${t.id}`} className="max-w-40 truncate">{t.title}</Button>
              <Button variant="ghost" size="sm" onClick={() => void del(t.id)} data-testid={`butler-del-${t.id}`} aria-label={`Delete ${t.title}`}><Trash2 className="size-3.5" /></Button>
            </div>
          ))}
        </div>
      )}
      {projectHint && (
        <div className="flex items-center gap-2 border-b px-4 py-1.5 text-xs text-muted-foreground">
          <span data-testid="butler-hint">looking at: {projectHint}</span>
          <button onClick={onClearHint} className="underline" data-testid="butler-hint-clear">clear</button>
        </div>
      )}
      <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-3">
        {turns.length === 0 && !busy && (
          <div className="flex flex-col gap-2">
            <p className="text-sm text-muted-foreground">Ask about your projects, or hand me setup work.</p>
            <div className="flex flex-wrap gap-1.5">
              {PRESETS.map((pr) => (
                <Button key={pr.label} variant="outline" size="sm" onClick={() => void send(pr.prompt)} data-testid={`butler-preset-${pr.label}`}>{pr.label}</Button>
              ))}
            </div>
          </div>
        )}
        {turns.map((t) => (
          <div key={`${t.turnId}-${t.prompt}`} className="flex flex-col gap-3" data-testid="butler-turn">
            <div className="flex justify-end">
              <div className="max-w-[85%] rounded-2xl bg-[#0084ff] px-4 py-2.5 text-sm text-white">{t.prompt}</div>
            </div>
            {t.turnId !== 'pending' && (
              <div className="flex gap-2.5">
                <Avatar className="mt-0.5 size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
                <div className="min-w-0 flex-1">
                  <div className="w-fit max-w-full rounded-2xl bg-muted px-4 py-2.5">
                    {t.error ? (<p className="text-sm text-destructive" data-testid="butler-error">{t.error}</p>)
                      : t.answer ? (<p className="text-sm whitespace-pre-wrap" data-testid="butler-answer">{t.answer}</p>)
                      : !busy ? (<p className="text-sm text-muted-foreground">…</p>) : null}
                  </div>
                  {t.steps && t.steps.length > 0 && <ButlerSteps steps={t.steps} />}
                </div>
              </div>
            )}
          </div>
        ))}
        {busy && (
          <div className="flex flex-col gap-2" data-testid="butler-pending">
            <div className="flex gap-2.5">
              <Avatar className="mt-0.5 size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
              <div className="flex w-fit items-center gap-1 rounded-2xl bg-muted px-4 py-3" aria-label="Butler is typing">
                <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground" />
                <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground [animation-delay:150ms]" />
                <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground [animation-delay:300ms]" />
              </div>
            </div>
            {lastStatus && (<p className="pl-9 text-xs text-muted-foreground" data-testid="butler-status">{lastStatus.tool} {lastStatus.status}…</p>)}
          </div>
        )}
        <div ref={bottomRef} />
      </div>
      {error && (<p className="shrink-0 border-t border-destructive/30 bg-destructive/10 px-4 py-2 text-xs break-all text-destructive" data-testid="butler-global-error">{error}</p>)}
      <div className="shrink-0 border-t p-3">
        <div className="flex w-full items-end gap-2 rounded-3xl border bg-muted px-2 py-1.5">
          <textarea placeholder="Ask Butler…" value={prompt} disabled={busy} onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); void send(prompt) } }}
            data-testid="butler-prompt" rows={1}
            className="max-h-36 min-h-10 flex-1 resize-none bg-transparent px-3 pt-2 pb-1 text-sm leading-relaxed focus:outline-none" />
          <Button size="icon" onClick={() => void send(prompt)} disabled={busy || !prompt.trim()} data-testid="butler-send" aria-label="Send" className="size-8 shrink-0 rounded-full"><Send className="size-4" /></Button>
        </div>
      </div>
    </div>
  )
}
