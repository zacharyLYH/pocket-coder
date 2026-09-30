import { useState, useEffect, useRef } from 'react'
import { ArrowUp, Bot, Check, ChevronDown, Copy, FileText, History, LoaderCircle, Plus, RotateCcw, Sparkles, Trash2 } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Avatar } from '@/components/ui/avatar'
import { Skeleton } from '@/components/ui/skeleton'
import { Separator } from '@/components/ui/separator'
import { api, projectPath } from '@/lib/api'
import { useThread } from '@/lib/useThread'
import type { AIConfigStatus, AgentStep, CodemapSection, CodemapThread, CodemapThreadSummary, CodemapTurn } from '@/lib/types'
import { FileOverlay } from '@/components/terminal/FileOverlay'
import { CodeBlock } from '@/components/CodeBlock'
import { Markdown } from '@/components/Markdown'
import { TurnSteps } from '@/components/TurnSteps'

const PRESETS = [
  { label: 'Explain the current diff', prompt: 'Explain the current diff. Read git status and the diff, then walk through each changed file.' },
  { label: 'Map this repo', prompt: 'Map this repo. Find the entrypoints, key directories, and data flow, then summarize how it fits together.' },
]

type OverlaySel = { path: string; start: number; end: number; sha: string }

function CopySnippet({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)
  useEffect(() => () => {
    if (timer.current) clearTimeout(timer.current)
  }, [])
  return (
    <button
      className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      aria-label="Copy snippet"
      onClick={(e) => {
        e.stopPropagation()
        void navigator.clipboard.writeText(text).catch(() => {})
        setCopied(true)
        if (timer.current) clearTimeout(timer.current)
        timer.current = setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  )
}

export function CodemapTab({ projectId, ai }: { projectId: string; ai: AIConfigStatus | null }) {
  const [prompt, setPrompt] = useState('')
  const [overlay, setOverlay] = useState<OverlaySel | null>(null)
  const viewportRef = useRef<HTMLDivElement>(null)

  const { threads, thread, inFlight, error, setError, openThread, newChat, removeThread, sendTurn, retryTurn } =
    useThread<CodemapThread, CodemapThreadSummary>({
      list: () => api<{ threads: CodemapThreadSummary[] }>(projectPath(projectId, '/codemap/threads')),
      open: async (id) => (await api<{ thread: CodemapThread }>(projectPath(projectId, `/codemap/threads/${encodeURIComponent(id)}`))).thread,
      send: (p, threadId) => api<{ threadId: string }>(projectPath(projectId, '/codemap'), { method: 'POST', body: JSON.stringify({ prompt: p, threadId }) }),
      retry: (tid) => api<{ threadId: string }>(projectPath(projectId, `/codemap/threads/${encodeURIComponent(tid)}/retry`), { method: 'POST', body: '{}' }),
      remove: (id) => api(projectPath(projectId, `/codemap/threads/${encodeURIComponent(id)}`), { method: 'DELETE' }).then(() => {}),
      autoOpen: true,
    }, projectId)

  const status = thread?.status ?? 'ready'
  const turns = thread?.turns ?? []
  const blocked = inFlight || status === 'running'

  useEffect(() => {
    const el = viewportRef.current
    if (!el) return
    const pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 96
    if (pinned || turns.length <= 1) el.scrollTop = el.scrollHeight
  }, [turns, inFlight])

  function generate() {
    const q = prompt.trim()
    if (!q || blocked) return
    if ([...q].length > 4000) {
      setError('Prompt over 4000 chars')
      return
    }
    setPrompt('')
    void sendTurn(q, thread?.id)
  }

  function retry() {
    if (!thread || inFlight) return
    void retryTurn(thread.id)
  }

  function formatTime(iso?: string): string {
    if (!iso) return ''
    const t = new Date(iso)
    return Number.isNaN(t.getTime()) ? '' : t.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }

  function SectionBlock({ s, index, sha, defaultOpen }: { s: CodemapSection; index: number; sha: string; defaultOpen: boolean }) {
    const [open, setOpen] = useState(defaultOpen)
    const refs = s.refs ?? []
    return (
      <div className="overflow-hidden rounded-xl border bg-muted/40" data-testid="codemap-section">
        <button
          className="flex min-h-[36px] w-full items-center gap-2 px-3 py-2 text-left"
          onClick={() => setOpen((v) => !v)}
          aria-expanded={open}
          data-testid="codemap-section-toggle"
        >
          <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-primary/10 font-mono text-[11px] font-semibold text-primary">
            {index + 1}
          </span>
          <span className="min-w-0 flex-1 truncate text-sm font-semibold">{s.title}</span>
          <span className="shrink-0 text-[11px] text-muted-foreground">
            {refs.length > 0 ? `${refs.length} ref${refs.length === 1 ? '' : 's'}` : ''}
          </span>
          <ChevronDown className={`size-3.5 shrink-0 text-muted-foreground transition-transform ${open ? 'rotate-180' : ''}`} />
        </button>
        {open && (
          <div className="border-t px-3 py-2">
            <div className="text-sm whitespace-pre-wrap">
              <Markdown text={s.summary} />
            </div>
            {refs.length > 0 && (
              <div className="mt-2 flex flex-col gap-2">
                {s.refs.map((r, j) => (
                  <div
                    key={j}
                    role="button"
                    tabIndex={0}
                    className="group rounded-xl border bg-background/80 px-3 py-2 text-left transition-colors hover:bg-muted"
                    onClick={() => setOverlay({ path: r.path, start: r.startLine, end: r.endLine, sha })}
                    onKeyDown={(e) => {
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        setOverlay({ path: r.path, start: r.startLine, end: r.endLine, sha })
                      }
                    }}
                    data-testid="codemap-ref"
                    data-path={r.path}
                  >
                    <span className="flex items-center gap-1.5 font-mono text-xs font-medium">
                      <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate">{r.path}</span>
                      {r.function && (
                        <span className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 text-[11px] font-semibold text-primary">
                          {r.function}()
                        </span>
                      )}
                      <span className="shrink-0 text-muted-foreground">L{r.startLine}-{r.endLine}</span>
                      <span className="flex-1" />
                      <CopySnippet text={r.snippet} />
                    </span>
                    <CodeBlock
                      code={r.snippet}
                      className="mt-1.5 overflow-x-auto rounded-lg bg-muted/60 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap"
                    />
                  </div>
                ))}
              </div>
            )}
          </div>
        )}
      </div>
    )
  }

  function renderSections(sections: CodemapSection[] | null, sha: string) {
    if (!sections || sections.length === 0) {
      return <p className="py-1 text-sm text-muted-foreground">No sections.</p>
    }
    const openFirstOnly = sections.length > 3
    return (
      <div className="flex flex-col gap-2">
        {sections.map((s, i) => (
          <SectionBlock key={i} s={s} index={i} sha={sha} defaultOpen={openFirstOnly ? i === 0 : true} />
        ))}
      </div>
    )
  }

  function FailCard({ title, error, hint, isLast, steps }: { title: string; error?: string | null; hint: string; isLast: boolean; steps?: AgentStep[] | null }) {
    return (
      <div className="flex flex-col gap-2 rounded-xl border border-destructive/30 bg-destructive/10 p-3">
        <p className="text-sm font-medium text-destructive">{title}</p>
        {error && <p className="text-xs break-words text-muted-foreground">{error}</p>}
        <p className="text-xs text-muted-foreground" data-testid="codemap-failed-hint">
          {hint}
        </p>
        {isLast && (
          <div>
            <Button size="sm" variant="outline" onClick={() => retry()} disabled={blocked} data-testid="codemap-retry">
              <RotateCcw className="size-3.5" />
              Retry turn
            </Button>
          </div>
        )}
        <TurnSteps steps={steps ?? []} testPrefix="codemap" />
      </div>
    )
  }

  function renderTurnBody(t: CodemapTurn, isLast: boolean) {
    const hasError = t.error !== undefined && t.error !== null && t.error !== ''
    const answerless = (t.sections == null || t.sections.length === 0) && !hasError
    if (hasError) {
      return <FailCard title="This turn failed." error={t.error} hint="The failed turn is saved. Retry it with the button below." isLast={isLast} steps={t.steps} />
    }
    if (answerless) {
      if (status === 'running') {
        return (
          <div className="flex flex-col gap-2 py-1" data-testid="codemap-spinner">
            <div className="flex items-center gap-2.5">
              <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
              <p className="text-xs text-muted-foreground">Searching the repo… progress lands in Logs.</p>
            </div>
            {isLast && (
              <div>
                <Button size="sm" variant="outline" disabled data-testid="codemap-retry">
                  <RotateCcw className="size-3.5" />
                  Retry turn
                </Button>
              </div>
            )}
          </div>
        )
      }
      return <FailCard title="This turn didn't finish." hint="The run crashed or the server restarted before it answered. Retry it with the button below." isLast={isLast} steps={t.steps} />
    }
    return (
      <>
        {renderSections(t.sections, t.sha)}
        <TurnSteps steps={t.steps ?? []} testPrefix="codemap" />
      </>
    )
  }

  return (
    <Card className="flex h-full w-full flex-col gap-0 overflow-hidden py-0 shadow-sm" data-testid="codemap-tab">
      <CardHeader className="shrink-0 px-3 py-2">
        <div className="flex items-center justify-between gap-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <DropdownMenu modal={false}>
              <DropdownMenuTrigger asChild>
                <Button
                  variant="ghost"
                  size="icon"
                  aria-label="Chat history"
                  data-testid="codemap-history-toggle"
                  className="size-7 shrink-0 text-muted-foreground"
                >
                  <History className="size-4" />
                </Button>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="start" className="w-72" data-testid="codemap-thread-list">
                <DropdownMenuLabel>Chats</DropdownMenuLabel>
                <DropdownMenuSeparator />
                {threads.length === 0 && (
                  <p className="px-2 py-1.5 text-xs text-muted-foreground">No chats yet — ask something below.</p>
                )}
                {threads.map((t) => (
                  <div key={t.id} data-testid="codemap-thread-item" data-thread-id={t.id} className="group flex items-center gap-0.5">
                    <DropdownMenuItem
                      onSelect={() => {
                        void openThread(t.id)
                      }}
                      className={`min-w-0 flex-1 items-start px-2 py-1.5 ${t.id === thread?.id ? 'bg-muted' : ''}`}
                    >
                      <span className="block min-w-0 flex-1">
                        <span className="block truncate text-[13px] font-medium">{t.title}</span>
                        <span className="block truncate text-[11px] text-muted-foreground">
                          {t.turnCount} turn{t.turnCount === 1 ? '' : 's'} · {formatTime(t.updatedAt)}
                          {t.preview ? ` · ${t.preview}` : ''}
                        </span>
                      </span>
                    </DropdownMenuItem>
                    <button
                      className="mr-1 flex size-6 shrink-0 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground focus-visible:opacity-100 md:opacity-0 md:group-hover:opacity-100"
                      aria-label={`Delete ${t.title}`}
                      data-testid="codemap-thread-delete"
                      disabled={blocked}
                      onClick={() => void removeThread(t.id)}
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            <Sparkles className="size-4 shrink-0 text-muted-foreground" />
            <CardTitle className="truncate text-base">{thread?.title || 'Codemap'}</CardTitle>
          </div>
          <Button variant="ghost" size="sm" onClick={newChat} disabled={inFlight} className="h-7 shrink-0 px-2 text-xs text-muted-foreground" data-testid="codemap-new-chat">
            <Plus className="size-3.5" />
            New chat
          </Button>
        </div>
      </CardHeader>
      <Separator className="shrink-0" />
      <CardContent className="flex min-h-0 flex-1 flex-col p-0">
        <div ref={viewportRef} className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3" data-testid="codemap-thread">
          {turns.length === 0 && !inFlight && status !== 'running' && (
            <div className="flex flex-1 flex-col items-center justify-center gap-2 text-center">
              <Avatar className="size-10">
                <Bot className="size-5 text-muted-foreground" />
              </Avatar>
              <p className="text-sm font-medium">Ask about this codebase</p>
              <p className="max-w-xs text-xs text-muted-foreground">
                Answers ground in search. Snippets open the exact file and lines.
              </p>
              <div className="mt-2 flex flex-wrap justify-center gap-2">
                {PRESETS.map((p) => (
                  <Button key={p.label} size="sm" variant="outline" className="rounded-full" onClick={() => setPrompt(p.prompt)} data-testid="codemap-preset">
                    {p.label}
                  </Button>
                ))}
              </div>
            </div>
          )}
          {turns.map((t, i) => (
            <div key={t.turnId} className="flex flex-col gap-3" data-testid="codemap-turn">
              <div className="flex justify-end">
                <div className="max-w-[85%] rounded-2xl bg-primary px-4 py-2.5 text-sm text-primary-foreground">
                  {t.prompt}
                </div>
              </div>
              <div className="flex gap-2.5">
                <Avatar className="mt-0.5 size-7">
                  <Bot className="size-4 text-muted-foreground" />
                </Avatar>
                <div className="min-w-0 flex-1">
                  {t.time && <p className="mb-1 text-[11px] text-muted-foreground">{formatTime(t.time)}</p>}
                  {renderTurnBody(t, i === turns.length - 1)}
                </div>
              </div>
            </div>
          ))}
          {inFlight && (
            <div className="flex gap-2.5">
              <Avatar className="mt-0.5 size-7">
                <LoaderCircle className="size-4 animate-spin text-muted-foreground" />
              </Avatar>
              <div className="flex flex-1 flex-col gap-2 pt-1">
                <Skeleton className="h-3.5 w-3/4" />
                <Skeleton className="h-3.5 w-full" />
                <Skeleton className="h-3.5 w-2/3" />
                <p className="text-xs text-muted-foreground">Searching the repo… progress lands in Logs.</p>
              </div>
            </div>
          )}
        </div>
        {error && (
          <p className="shrink-0 border-t border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive" data-testid="codemap-error">
            {error}
          </p>
        )}
        <div className="shrink-0 p-3 pt-1">
          <div className="flex w-full flex-col rounded-3xl border bg-muted">
            <textarea
              placeholder="Ask anything..."
              value={prompt}
              disabled={blocked}
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  generate()
                }
              }}
              data-testid="codemap-prompt"
              rows={2}
              className="max-h-36 min-h-10 w-full resize-none bg-transparent px-5 pt-3.5 pb-2 text-sm leading-relaxed focus:outline-none"
            />
            <div className="flex items-center justify-end px-2.5 pb-2.5">
              <Button
                size="icon"
                onClick={() => generate()}
                disabled={blocked || !prompt.trim() || ai?.configured === false}
                data-testid="codemap-generate"
                aria-label="Send"
                className="size-8 shrink-0 rounded-full"
              >
                <ArrowUp className="size-4" />
              </Button>
            </div>
          </div>
          {ai?.configured === false && (
            <p className="mt-1.5 px-2 text-xs text-muted-foreground">Add a model key in Home → AI to enable codemaps.</p>
          )}
        </div>
      </CardContent>
      {overlay && (
        <FileOverlay
          projectId={projectId}
          path={overlay.path}
          start={overlay.start}
          end={overlay.end}
          sha={overlay.sha}
          onBack={() => setOverlay(null)}
        />
      )}
    </Card>
  )
}
