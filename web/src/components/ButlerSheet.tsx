import { useEffect, useRef, useState } from 'react'
import { Bot, Plus, RotateCcw, Send, Trash2, X } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Avatar } from '@/components/ui/avatar'
import { ButlerConfirm } from '@/components/ButlerConfirm'
import { Markdown } from '@/components/Markdown'
import { TurnSteps } from '@/components/TurnSteps'
import { api } from '@/lib/api'
import { postButlerRetry, postButlerTurn } from '@/lib/butler'
import { useThread } from '@/lib/useThread'
import type { ButlerThread, ButlerThreadSummary } from '@/lib/types'

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
  const [prompt, setPrompt] = useState('')
  const [notice, setNotice] = useState<string | null>(null)
  const bottomRef = useRef<HTMLDivElement>(null)

  const { threads, thread, inFlight, error, openThread, newChat, removeThread, sendTurn, retryTurn } =
    useThread<ButlerThread, ButlerThreadSummary>({
      list: () => api<{ threads: ButlerThreadSummary[] }>('/api/butler/threads'),
      open: async (id) => (await api<{ thread: ButlerThread }>(`/api/butler/threads/${encodeURIComponent(id)}`)).thread,
      send: (p, threadId) => postButlerTurn({ prompt: p, threadId, projectHint: projectHint ?? undefined }),
      retry: (tid) => postButlerRetry(tid),
      remove: (id) => api(`/api/butler/threads/${encodeURIComponent(id)}`, { method: 'DELETE' }).then(() => {}),
    })

  useEffect(() => {
    try { bottomRef.current?.scrollIntoView({ behavior: 'smooth', block: 'end' }) } catch { /* jsdom has no layout */ }
  }, [thread?.turns.length, inFlight])

  // Server owns sendability. The starter posts; everyone (including a
  // reloaded starter) polls the open thread until it settles. The reserve
  // persists the prompt before the POST returns, so the user's bubble is
  // the transcript's own turn — no client-side echo to keep in sync.
  const status = thread?.status ?? 'ready'
  const confirms = thread?.approvals ?? []
  const blocked = inFlight || status === 'running' || status === 'awaiting'
  const showPending = inFlight || status === 'running'

  function send(text: string) {
    const p = text.trim()
    if (!p || blocked) return
    setNotice(null)
    setPrompt('')
    void sendTurn(p, thread?.id).catch(() => {})
  }

  const turns = thread?.turns ?? []
  const lastFailedTurn = !inFlight && status !== 'awaiting' && thread && turns.length > 0 && turns[turns.length - 1].error ? turns[turns.length - 1] : null

  function retry() {
    if (!lastFailedTurn || !thread) return
    void retryTurn(thread.id)
  }

  function del(id: string) {
    setNotice(null)
    void removeThread(id)
  }

  function startNew() {
    setNotice(null)
    newChat()
  }

  return (
    <div className="flex h-full flex-col" data-testid="butler-sheet">
      <div className="flex items-center gap-2 border-b px-4 py-3">
        <Avatar className="size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
        <h2 className="text-[15px] font-semibold tracking-tight">Butler</h2>
        <span className="flex-1" />
        <Button variant="ghost" size="sm" onClick={startNew} data-testid="butler-new" aria-label="New chat"><Plus className="size-4" /></Button>
        <Button variant="ghost" size="sm" onClick={onClose} data-testid="butler-close" aria-label="Close"><X className="size-4" /></Button>
      </div>
      {threads.length > 0 && (
        <div className="flex gap-1.5 overflow-x-auto border-b px-3 py-2" data-testid="butler-threads">
          {threads.map((t) => (
            <div key={t.id} className="flex shrink-0 items-center gap-1">
              <Button variant={thread?.id === t.id ? 'secondary' : 'ghost'} size="sm" onClick={() => void openThread(t.id)} data-testid={`butler-thread-${t.id}`} className="max-w-40 truncate">{t.title}</Button>
              <Button variant="ghost" size="sm" onClick={() => void del(t.id)} disabled={inFlight} data-testid={`butler-del-${t.id}`} aria-label={`Delete ${t.title}`}><Trash2 className="size-3.5" /></Button>
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
        {turns.length === 0 && !showPending && (
          <div className="flex flex-col gap-2">
            <p className="text-sm text-muted-foreground">Ask about your projects, or hand me setup work.</p>
            <div className="flex flex-wrap gap-1.5">
              {PRESETS.map((pr) => (
                <Button key={pr.label} variant="outline" size="sm" onClick={() => send(pr.prompt)} data-testid={`butler-preset-${pr.label}`}>{pr.label}</Button>
              ))}
            </div>
          </div>
        )}
        {turns.map((t) => (
          <div key={t.turnId} className="flex flex-col gap-3" data-testid="butler-turn">
            <div className="flex justify-end">
              <div className="max-w-[85%] rounded-2xl bg-[#0084ff] px-4 py-2.5 text-sm text-white">{t.prompt}</div>
            </div>
            <div className="flex gap-2.5">
              <Avatar className="mt-0.5 size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
              <div className="min-w-0 flex-1">
                <div className="w-fit max-w-full rounded-2xl bg-muted px-4 py-2.5">
                  {t.error ? (<p className="text-sm text-destructive" data-testid="butler-error">{t.error}</p>)
                    : t.answer ? (<div data-testid="butler-answer"><Markdown className="text-sm" text={t.answer} /></div>)
                    : (<p className="text-sm text-muted-foreground">…</p>)}
                  {lastFailedTurn?.turnId === t.turnId && (
                    <Button variant="outline" size="sm" className="mt-2" onClick={() => retry()} disabled={inFlight} data-testid="butler-retry">
                      <RotateCcw className="size-3.5" /> Retry
                    </Button>
                  )}
                </div>
                {t.steps && t.steps.length > 0 && <TurnSteps steps={t.steps} testPrefix="butler" />}
              </div>
            </div>
          </div>
        ))}
        {confirms.map((c) => (
          <ButlerConfirm
            key={c.id}
            card={c}
            onDone={(result) => {
              if (result) setNotice(result)
              if (thread) void openThread(thread.id)
            }}
          />
        ))}
        {notice && <p className="text-xs text-muted-foreground" data-testid="butler-applied">{notice}</p>}
        {showPending && (
          <div className="flex gap-2.5" data-testid="butler-pending">
            <Avatar className="mt-0.5 size-7"><Bot className="size-4 text-muted-foreground" /></Avatar>
            <div className="flex w-fit items-center gap-1 rounded-2xl bg-muted px-4 py-3" aria-label="Butler is typing">
              <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground" />
              <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground [animation-delay:150ms]" />
              <span className="size-1.5 animate-bounce rounded-full bg-muted-foreground [animation-delay:300ms]" />
            </div>
          </div>
        )}
      </div>
      {error && (<p className="shrink-0 border-t border-destructive/30 bg-destructive/10 px-4 py-2 text-xs break-all text-destructive" data-testid="butler-global-error">{error}</p>)}
      <div className="shrink-0 border-t p-3">
        {status === 'awaiting' && <p className="mb-2 px-3 text-xs text-muted-foreground" data-testid="butler-approval-block">Confirm or discard the pending action to continue.</p>}
        {status === 'running' && <p className="mb-2 px-3 text-xs text-muted-foreground" data-testid="butler-running-block">A turn is already running in this thread — it will appear here when it finishes.</p>}
        <div className="flex w-full items-end gap-2 rounded-3xl border bg-muted px-2 py-1.5">
          <textarea placeholder="Ask Butler…" value={prompt} disabled={blocked} onChange={(e) => setPrompt(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send(prompt) } }}
            data-testid="butler-prompt" rows={1}
            className="max-h-36 min-h-10 flex-1 resize-none bg-transparent px-3 pt-2 pb-1 text-base leading-relaxed focus:outline-none disabled:cursor-not-allowed" />
          <Button size="icon" onClick={() => send(prompt)} disabled={blocked || !prompt.trim()} data-testid="butler-send" aria-label="Send" className="size-8 shrink-0 rounded-full disabled:cursor-not-allowed"><Send className="size-4" /></Button>
        </div>
      </div>
    </div>
  )
}
