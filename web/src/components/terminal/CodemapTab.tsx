import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowUp, Bot, Check, ChevronDown, Copy, FileText, LoaderCircle, Sparkles, Wrench } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Avatar } from '@/components/ui/avatar'
import { Skeleton } from '@/components/ui/skeleton'
import { Separator } from '@/components/ui/separator'
import { api, errMsg, projectPath } from '@/lib/api'
import type { CodemapSection, CodemapToolCall, CodemapTurn } from '@/lib/types'
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
// assistant-ui thread. User prompts right, answers left with an avatar,
// composer docked at the bottom. History loads once; new turns append.
export function CodemapTab({ projectId }: { projectId: string }) {
  const { status: ai } = useAiConfig()
  const [turns, setTurns] = useState<CodemapTurn[]>([])
  const [prompt, setPrompt] = useState(() => {
    try {
      return localStorage.getItem(`pcoder-codemap-draft-${projectId}`) ?? ''
    } catch {
      return ''
    }
  })
  const [followup, setFollowup] = useState<CodemapTurn | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [overlay, setOverlay] = useState<OverlaySel | null>(null)
  const viewportRef = useRef<HTMLDivElement>(null)

  const loadHistory = useCallback(async () => {
    try {
      const d = await api<{ turns: CodemapTurn[] }>(projectPath(projectId, '/codemap/history?limit=20'))
      setTurns(d.turns ?? [])
    } catch {
      // history is a nicety; a fresh thread still works
    }
  }, [projectId])

  useEffect(() => {
    void loadHistory()
  }, [loadHistory])

  useEffect(() => {
    try {
      localStorage.setItem(`pcoder-codemap-draft-${projectId}`, prompt)
    } catch {
      // private browsing: draft just does not persist
    }
  }, [prompt, projectId])

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
    setBusy(true)
    setError(null)
    try {
      const history = followup
        ? [
            { role: 'user', content: followup.prompt },
            { role: 'assistant', content: JSON.stringify(followup.sections ?? []) },
          ]
        : []
      const d = await api<{ turnId: string; sha: string; sections: CodemapSection[]; tools: CodemapToolCall[] }>(
        projectPath(projectId, '/codemap'),
        { method: 'POST', body: JSON.stringify({ prompt: q, history }) },
      )
      setTurns((prev) => [...prev, { turnId: d.turnId, sha: d.sha, prompt: q, sections: d.sections, tools: d.tools ?? [] }])
      setPrompt('')
      setFollowup(null)
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  function renderSections(sections: CodemapSection[] | null, sha: string) {    if (!sections || sections.length === 0) {
      return <p className="py-1 text-sm text-muted-foreground">No sections.</p>
    }
    return (
      <div className="flex flex-col gap-4">
        {sections.map((s, i) => (
          <div key={i}>
            <p className="text-sm font-semibold">{s.title}</p>
            <p className="mt-1 text-sm whitespace-pre-wrap">{s.summary}</p>
            {(s.refs ?? []).length > 0 && (
              <div className="mt-2 flex flex-col gap-2">
                {s.refs.map((r, j) => (
                  <button
                    key={j}
                    className="group rounded-xl border bg-muted/40 px-3 py-2 text-left transition-colors hover:bg-muted"
                    onClick={() => setOverlay({ path: r.path, start: r.startLine, end: r.endLine, sha })}
                    data-testid="codemap-ref"
                    data-path={r.path}
                  >
                    <span className="flex items-center gap-1.5 font-mono text-xs font-medium">
                      <FileText className="size-3.5 shrink-0 text-muted-foreground" />
                      <span className="truncate">{r.path}</span>
                      <span className="shrink-0 text-muted-foreground">L{r.startLine}-{r.endLine}</span>
                      <span className="flex-1" />
                      <CopySnippet text={r.snippet} />
                    </span>
                    <pre className="mt-1.5 overflow-x-auto rounded-lg bg-background/80 p-2 font-mono text-[11px] leading-relaxed whitespace-pre-wrap">
                      {r.snippet}
                    </pre>
                  </button>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    )
  }

  return (
    <Card className="flex h-full w-full flex-col overflow-hidden shadow-sm" data-testid="codemap-tab">
      <CardHeader className="shrink-0 pb-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Sparkles className="size-4 text-muted-foreground" />
            <CardTitle className="text-base">Codemap</CardTitle>
          </div>
          {turns.length > 0 && (
            <Button variant="ghost" size="sm" onClick={() => setTurns([])} className="h-7 px-2 text-xs text-muted-foreground">
              Clear
            </Button>
          )}
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
                  {renderSections(t.sections, t.sha)}
                  <Steps tools={t.tools ?? []} />
                  <Button
                    size="sm"
                    variant="ghost"
                    className="mt-1 h-7 px-2 text-xs text-muted-foreground"
                    onClick={() => {
                      setFollowup(t)
                      viewportRef.current?.scrollTo({ top: viewportRef.current.scrollHeight })
                    }}
                    data-testid="codemap-followup"
                  >
                    Follow up
                  </Button>
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
          {followup && (
            <p className="mb-1.5 px-2 text-xs text-muted-foreground">
              Following up on “{followup.prompt.slice(0, 60)}”{' '}
              <button className="underline" onClick={() => setFollowup(null)}>clear</button>
            </p>
          )}
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
