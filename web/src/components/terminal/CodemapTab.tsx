import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowUp, Bot, Check, ChevronDown, Copy, FileText, History, LoaderCircle, Plus, Sparkles, Trash2, Wrench } from 'lucide-react'

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
import { ApiError, api, errMsg, projectPath } from '@/lib/api'
import type { CodemapSection, CodemapThread, CodemapThreadSummary, CodemapToolCall, CodemapTurn } from '@/lib/types'
import { useAiConfig } from '@/hooks/useAiConfig'
import { FileOverlay } from '@/components/terminal/FileOverlay'

// Suggestion chips double as the empty-state welcome (the assistant-ui
// ThreadWelcome + Suggestions shape): they show what good input looks
// like and prefill the composer. Free text stays for the odd traces.
const PRESETS = [
  { label: 'Explain the current diff', prompt: 'Explain the current diff. Read git status and the diff, then walk through each changed file.' },
  { label: 'Map this repo', prompt: 'Map this repo. Find the entrypoints, key directories, and data flow, then summarize how it fits together.' },
]

type OverlaySel = { path: string; start: number; end: number; sha: string }

// Steps shows the tool calls behind a turn: name plus full args, one row
// each. Collapsed by default; this is the first place to look when an
// answer looks wrong. Full outputs live in the Logs tab.
function Steps({ tools }: { tools: CodemapToolCall[] }) {
  const [open, setOpen] = useState(false)
  if (tools.length === 0) return null
  return (
    <div className="mt-2 overflow-hidden rounded-xl border bg-muted/40">
      <button
        className="flex min-h-[36px] w-full items-center gap-1.5 px-3 py-1.5 text-left text-xs text-muted-foreground"
        onClick={() => setOpen((v) => !v)}
        aria-expanded={open}
        data-testid="codemap-steps-toggle"
      >
        <Wrench className="size-3.5" />
        {tools.length} step{tools.length === 1 ? '' : 's'}
        <ChevronDown className={`size-3.5 transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="flex flex-col gap-1 border-t p-2" data-testid="codemap-steps">
          {tools.map((c, i) => (
            <div key={i} className="rounded-lg bg-background/80 px-2 py-1.5">
              <p className="font-mono text-[11px] font-medium">{c.tool}</p>
              <p className="mt-0.5 font-mono text-[11px] break-all text-muted-foreground">{c.args}</p>
              {(c.output || c.error) && (
                <pre className="mt-1 max-h-40 overflow-y-auto rounded-md bg-muted/60 p-1.5 font-mono text-[11px] whitespace-pre-wrap text-muted-foreground">
                  {c.error ? `error: ${c.error}` : c.output}
                </pre>
              )}
            </div>
          ))}
          <p className="px-1 text-[11px] text-muted-foreground">Full outputs land in the Logs tab while it runs.</p>
        </div>
      )}
    </div>
  )
}

function CopySnippet({ text }: { text: string }) {
  const [copied, setCopied] = useState(false)
  return (
    <button
      className="flex size-7 items-center justify-center rounded-md text-muted-foreground hover:bg-accent hover:text-accent-foreground"
      aria-label="Copy snippet"
      onClick={(e) => {
        e.stopPropagation()
        void navigator.clipboard.writeText(text).catch(() => {})
        setCopied(true)
        setTimeout(() => setCopied(false), 1500)
      }}
    >
      {copied ? <Check className="size-3.5" /> : <Copy className="size-3.5" />}
    </button>
  )
}

// Codemap tab: ask-about-the-code over the agent loop, laid out like an
// assistant-ui thread. One chat per thread, many threads per project —
// each thread is its own server-side file, listed in the history menu.
// The active thread's turns render here; the server rebuilds context
// from the persisted thread file on every send.
export function CodemapTab({ projectId }: { projectId: string }) {
  const { status: ai } = useAiConfig()
  const [threads, setThreads] = useState<CodemapThreadSummary[]>([])
  // No localStorage: the server is the source of truth. Mount opens the
  // newest thread; the composer starts empty every time.
  const [activeId, setActiveId] = useState<string | null>(null)
  const [turns, setTurns] = useState<CodemapTurn[]>([])
  const [title, setTitle] = useState<string>('')
  const [menuOpen, setMenuOpen] = useState(false)
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [overlay, setOverlay] = useState<OverlaySel | null>(null)
  const viewportRef = useRef<HTMLDivElement>(null)
  // Mirror of activeId for the in-flight guard: generate() captures the
  // thread it sent for, and only appends when the user is still viewing
  // that same thread at completion.
  const activeIdRef = useRef<string | null>(null)
  useEffect(() => {
    activeIdRef.current = activeId
  }, [activeId])

  const loadThreads = useCallback(async () => {
    try {
      const d = await api<{ threads: CodemapThreadSummary[] }>(projectPath(projectId, '/codemap/threads'))
      setThreads(d.threads ?? [])
      return d.threads ?? []
    } catch {
      return null
    }
  }, [projectId])

  const openThread = useCallback(async (id: string) => {
    try {
      const d = await api<{ thread: CodemapThread }>(projectPath(projectId, `/codemap/threads/${encodeURIComponent(id)}`))
      setActiveId(d.thread.id)
      setTitle(d.thread.title)
      setTurns(d.thread.turns ?? [])
      setError(null)
    } catch (e) {
      setError(errMsg(e))
    }
  }, [projectId])

  useEffect(() => {
    let cancelled = false
    void (async () => {
      const list = await loadThreads()
      if (cancelled || !list) return
      if (list.length > 0) {
        void openThread(list[0].id)
      }
      // else: no chats yet — pending state until the first send
    })()
    return () => {
      cancelled = true
    }
  }, [projectId, loadThreads, openThread])

  // Follow new turns only while pinned to the bottom, so reading history
  // never yanks the scroll out from under you (same rule as LogsTab).
  useEffect(() => {
    const el = viewportRef.current
    if (!el) return
    const pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 96
    if (pinned || turns.length <= 1) el.scrollTop = el.scrollHeight
  }, [turns, busy])

  async function generate() {
    const q = prompt.trim()
    if (!q || busy) return
    // Capture the thread this prompt belongs to: if the user switches
    // threads mid-run, the finished answer must not land in the wrong
    // transcript (it is already persisted server-side under sendThreadId).
    const sendThreadId = activeIdRef.current
    // Clear the composer on send so the thread feels instant; restore the
    // text only if the request fails.
    setPrompt('')
    setBusy(true)
    setError(null)
    try {
      // Server-authoritative context: the thread file is both the record
      // and the context source. The client sends only the prompt; the
      // server rebuilds history from the persisted thread.
      const d = await api<{ turnId: string; sha: string; sections: CodemapSection[]; tools: CodemapToolCall[]; threadId?: string; threadTitle?: string; time?: string }>(
        projectPath(projectId, '/codemap'),
        { method: 'POST', body: JSON.stringify({ prompt: q, threadId: sendThreadId ?? undefined }) },
      )
      // Still viewing the thread this prompt was sent for? Append with
      // the server timestamp so the turn shows its time immediately.
      // Otherwise leave the visible transcript alone — the turn is
      // already persisted under its own thread file.
      const nowIso = d.time ?? new Date().toISOString()
      const turn = { turnId: d.turnId, sha: d.sha, prompt: q, sections: d.sections, tools: d.tools ?? [], time: nowIso }
      if (sendThreadId != null) {
        if (activeIdRef.current === sendThreadId) {
          setTurns((prev) => [...prev, turn])
          if (d.threadTitle) setTitle(d.threadTitle)
        }
      } else {
        if (activeIdRef.current == null && d.threadId) {
          setActiveId(d.threadId)
          setTurns([turn])
          if (d.threadTitle) setTitle(d.threadTitle)
        }
      }
      void loadThreads()
    } catch (e) {
      // The server keeps the thread file even on failure (created before
      // the run) and returns its id: adopt it so the empty chat is
      // selected and retryable instead of lost.
      if (e instanceof ApiError && sendThreadId == null && e.body?.threadId && activeIdRef.current == null) {
        setActiveId(e.body.threadId)
        if (e.body?.threadTitle) setTitle(e.body.threadTitle)
        setTurns([])
        void loadThreads()
      }
      setError(errMsg(e))
      setPrompt(q)
    } finally {
      setBusy(false)
    }
  }

  function newChat() {
    setActiveId(null)
    setTitle('')
    setTurns([])
    setError(null)
    setMenuOpen(false)
  }

  async function deleteThread(id: string) {
    try {
      await api(projectPath(projectId, `/codemap/threads/${encodeURIComponent(id)}`), { method: 'DELETE' })
    } catch (e) {
      setError(errMsg(e))
      return
    }
    setThreads((prev) => prev.filter((t) => t.id !== id))
    if (id === activeId) newChat()
  }

  function formatTime(iso?: string): string {
    if (!iso) return ''
    const t = new Date(iso)
    return Number.isNaN(t.getTime()) ? '' : t.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  }

  // SectionBlock renders one Devin-style flow step: a numbered, succinct
  // explanation with its code refs nested beneath. Sections collapse so
  // big discussions stay scannable; small answers stay fully open.
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
            <p className="text-sm whitespace-pre-wrap">{s.summary}</p>
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
                    <pre className="mt-1.5 overflow-x-auto rounded-lg bg-muted/60 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap">
                      {r.snippet}
                    </pre>
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
    // Small answers read fully open; big ones open only the first step.
    const openFirstOnly = sections.length > 3
    return (
      <div className="flex flex-col gap-2">
        {sections.map((s, i) => (
          <SectionBlock key={i} s={s} index={i} sha={sha} defaultOpen={openFirstOnly ? i === 0 : true} />
        ))}
      </div>
    )
  }

  return (
    <Card className="flex h-full w-full flex-col gap-0 overflow-hidden py-0 shadow-sm" data-testid="codemap-tab">
      <CardHeader className="shrink-0 px-3 py-2">
        <div className="flex items-center justify-between gap-1">
          <div className="flex min-w-0 items-center gap-1.5">
            <DropdownMenu open={menuOpen} onOpenChange={setMenuOpen} modal={false}>
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
                  // Delete sits beside the item, never inside it: a nested
                  // button's click never dispatches (Radix selects and
                  // unmounts the menu on pointer-up first), so nesting
                  // would silently swallow deletes.
                  <div key={t.id} data-testid="codemap-thread-item" data-thread-id={t.id} className="group flex items-center gap-0.5">
                    <DropdownMenuItem
                      onSelect={() => {
                        void openThread(t.id)
                      }}
                      className={`min-w-0 flex-1 items-start px-2 py-1.5 ${t.id === activeId ? 'bg-muted' : ''}`}
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
                      onClick={() => void deleteThread(t.id)}
                    >
                      <Trash2 className="size-3.5" />
                    </button>
                  </div>
                ))}
              </DropdownMenuContent>
            </DropdownMenu>
            <Sparkles className="size-4 shrink-0 text-muted-foreground" />
            <CardTitle className="truncate text-base">{title || 'Codemap'}</CardTitle>
          </div>
          <Button variant="ghost" size="sm" onClick={newChat} disabled={busy} className="h-7 shrink-0 px-2 text-xs text-muted-foreground" data-testid="codemap-new-chat">
            <Plus className="size-3.5" />
            New chat
          </Button>
        </div>
      </CardHeader>
      <Separator className="shrink-0" />
      <CardContent className="flex min-h-0 flex-1 flex-col p-0">
        <div ref={viewportRef} className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-3" data-testid="codemap-thread">
          {turns.length === 0 && !busy && (
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
          {turns.map((t) => (
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
                  {renderSections(t.sections, t.sha)}
                  <Steps tools={t.tools ?? []} />
                </div>
              </div>
            </div>
          ))}
          {busy && (
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
              onChange={(e) => setPrompt(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && !e.shiftKey) {
                  e.preventDefault()
                  void generate()
                }
              }}
              data-testid="codemap-prompt"
              rows={2}
              className="max-h-36 min-h-10 w-full resize-none bg-transparent px-5 pt-3.5 pb-2 text-sm leading-relaxed focus:outline-none"
            />
            <div className="flex items-center justify-end px-2.5 pb-2.5">
              <Button
                size="icon"
                onClick={() => void generate()}
                disabled={busy || !prompt.trim() || ai?.configured === false}
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
