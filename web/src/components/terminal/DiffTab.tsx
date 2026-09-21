import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { DiffModeEnum, DiffView } from '@git-diff-view/react'
import '@git-diff-view/react/styles/diff-view.css'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { api, ApiError, errMsg, projectPath } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { BranchSheet } from '@/components/terminal/BranchSheet'
import type { GitBranchList, GitDiffResponse, GitFileStatus, GitStatusResponse } from '@/lib/types'

// Context levels for the per-file progressive expansion (§1.1).
const CONTEXT_LEVELS = [3, 10, 30, 0] as const // 0 = full
const contextParam = (n: number) => (n === 0 ? 'full' : String(n))

// parseHunks splits raw unified diff text into its @@ hunks, dropping the
// `diff --git` / `---` / `+++` preamble. Each entry keeps its @@ header so
// it renders standalone and stages as an independent patch.
export function parseHunks(diff: string): string[] {
  const hunks: string[] = []
  let current: string[] | null = null
  for (const line of diff.split('\n')) {
    if (line.startsWith('@@ ')) {
      if (current) hunks.push(current.join('\n'))
      current = [line]
    } else if (current) {
      current.push(line)
    }
  }
  if (current) hunks.push(current.join('\n'))
  return hunks.filter((h) => h.trim() !== '')
}

// hunkPatch wraps one hunk in a minimal file patch git apply understands.
export function hunkPatch(path: string, hunk: string): string {
  return `diff --git a/${path} b/${path}\n--- a/${path}\n+++ b/${path}\n${hunk}\n`
}

type FileDiff = {
  diff: string
  truncated: boolean
  oldContent: string
  newContent: string
  contentsTruncated: boolean
  binary: boolean
  loading: boolean
  error: string | null
}

const emptyFileDiff: FileDiff = {
  diff: '',
  truncated: false,
  oldContent: '',
  newContent: '',
  contentsTruncated: false,
  binary: false,
  loading: false,
  error: null,
}

// HunkView renders one hunk's viewer. The data object is memoized on the
// blob strings: DiffView builds a fresh DiffFile (and an empty flash while
// it rebuilds) whenever data identity changes, so without this every
// 5-second status poll would blink every expanded hunk.
const HunkView = memo(function HunkView({ path, hunk, oldContent, newContent }: {
  path: string
  hunk: string
  oldContent: string
  newContent: string
}) {
  const data = useMemo(
    () => ({
      oldFile: { fileName: path, content: oldContent },
      newFile: { fileName: path, content: newContent },
      // Each entry must be a complete mini-diff with its ---/+++ header:
      // the parser finds hunks only after those lines, so a bare @@ hunk
      // renders empty.
      hunks: [hunkPatch(path, hunk)],
    }),
    [path, hunk, oldContent, newContent],
  )
  return (
    <DiffView
      data={data}
      diffViewMode={DiffModeEnum.Unified}
      diffViewWrap={true}
      diffViewFontSize={12}
      diffViewHighlight={true}
      diffViewAddWidget={false}
    />
  )
})

// Diff tab: file list with per-file and per-hunk staging, plus a copy-only
// Ask-AI notepad. Unified view only with wrapping: split view cannot win
// on a phone. Polls status like PreviewTab; per-file diffs load on expand.
export function DiffTab({ projectId, onSelectView }: { projectId: string; onSelectView?: (v: 'diff' | 'preview' | 'nerdy' | 'codemap') => void }) {
  const [status, setStatus] = useState<GitStatusResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const [diffs, setDiffs] = useState<Record<string, FileDiff>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [notesOpen, setNotesOpen] = useState(false)
  const [notes, setNotes] = useState(() => {
    try {
      return localStorage.getItem(`pcoder-diff-notes-${projectId}`) ?? ''
    } catch {
      return ''
    }
  })
  const [copied, setCopied] = useState(false)

  // Git tab additions (mobile-git-flow-rfc).
  const [contextByPath, setContextByPath] = useState<Record<string, number>>({})
  const [message, setMessage] = useState('')
  const [identityOpen, setIdentityOpen] = useState(false)
  const [idName, setIdName] = useState('')
  const [idEmail, setIdEmail] = useState('')
  const [commitSha, setCommitSha] = useState<string | null>(null)
  const [gitBusy, setGitBusy] = useState<string | null>(null)
  const [gitError, setGitError] = useState<string | null>(null)
  const [prDesc, setPrDesc] = useState<{ title: string; body: string } | null>(null)
  const [prState, setPrState] = useState<'idle' | 'pushing' | 'pushed' | 'rejected'>('idle')
  const [branchOpen, setBranchOpen] = useState(false)
  const [branches, setBranches] = useState<GitBranchList | null>(null)
  const [newBranch, setNewBranch] = useState('')

  const refresh = useCallback(async (): Promise<GitStatusResponse | null> => {
    try {
      const d = await api<GitStatusResponse>(projectPath(projectId, '/git/status'))
      setStatus(d)
      setError(null)
      return d
    } catch (e) {
      // Keep the last-known list: a transient probe failure should not
      // blink the file list away mid-read.
      setError(errMsg(e))
      return null
    }
  }, [projectId])

  useEffect(() => {
    void refresh()
    const id = setInterval(() => void refresh(), 5000)
    return () => clearInterval(id)
  }, [refresh])

  useEffect(() => {
    try {
      localStorage.setItem(`pcoder-diff-notes-${projectId}`, notes)
    } catch {
      // Private browsing: notes just do not persist.
    }
  }, [notes, projectId])

  async function loadDiff(path: string, staged: boolean, contextLevel?: number) {
    const un = contextLevel ?? contextByPath[path] ?? 3
    const key = `${staged ? 's' : 'u'}:${path}`
    setDiffs((prev) => ({ ...prev, [key]: { ...emptyFileDiff, loading: true } }))
    try {
      const d = await api<GitDiffResponse>(
        projectPath(
          projectId,
          `/git/diff?path=${encodeURIComponent(path)}&staged=${staged}&context=${contextParam(un)}`,
        ),
      )
      setDiffs((prev) => ({
        ...prev,
        [key]: {
          diff: d.diff,
          truncated: d.truncated,
          oldContent: d.oldContent ?? '',
          newContent: d.newContent ?? '',
          contentsTruncated: d.contentsTruncated ?? false,
          binary: d.binary ?? false,
          loading: false,
          error: null,
        },
      }))
    } catch (e) {
      setDiffs((prev) => ({ ...prev, [key]: { ...emptyFileDiff, error: errMsg(e) } }))
    }
  }

  function toggle(path: string, f: GitFileStatus) {
    const next = !expanded[path]
    setExpanded((prev) => ({ ...prev, [path]: next }))
    if (!next) return
    if ((f.unstaged !== ' ' || f.staged === '?') && !diffs[`u:${path}`]) void loadDiff(path, false)
    if (f.staged !== ' ' && f.staged !== '?') {
      if (!diffs[`s:${path}`]) void loadDiff(path, true)
    }
  }

  // bumpContext moves one file through 3 → 10 → 30 → full and refetches.
  function bumpContext(path: string) {
    const cur = contextByPath[path] ?? 3
    const idx = CONTEXT_LEVELS.indexOf(cur as (typeof CONTEXT_LEVELS)[number])
    const next = CONTEXT_LEVELS[Math.min(idx + 1, CONTEXT_LEVELS.length - 1)]
    setContextByPath((prev) => ({ ...prev, [path]: next }))
    const f = status?.files.find((x) => x.path === path)
    if (!f) return
    if (f.unstaged !== ' ' || f.staged === '?') void loadDiff(path, false, next)
    if (f.staged !== ' ' && f.staged !== '?') void loadDiff(path, true, next)
  }

  // collapseAll resets every file's expansion + context (ephemeral).
  function collapseAll() {
    setExpanded({})
    setContextByPath({})
  }

  // reloadExpanded refetches both diff sections for every expanded file
  // still present after a stage/unstage mutation. Without this an expanded
  // file keeps showing its pre-mutation hunks (or a stale loading state).
  async function reloadExpanded(fresh: GitStatusResponse | null) {
    if (!fresh) return
    const byPath = new Map(fresh.files.map((f) => [f.path, f]))
    await Promise.all(
      Object.keys(expanded).flatMap((path) => {
        const f = byPath.get(path)
        if (!f || !expanded[path]) return []
        const jobs: Promise<void>[] = []
        if (f.unstaged !== ' ' || f.staged === '?') jobs.push(loadDiff(path, false))
        if (f.staged !== ' ' && f.staged !== '?') jobs.push(loadDiff(path, true))
        return jobs
      }),
    )
  }

  async function stageFile(path: string, unstage: boolean) {
    const key = `file:${path}:${unstage ? 'u' : 's'}`
    setBusy(key)
    setError(null)
    try {
      await api(projectPath(projectId, `/git/${unstage ? 'unstage' : 'stage'}`), {
        method: 'POST',
        body: JSON.stringify({ path }),
      })
      setDiffs((prev) => {
        const next = { ...prev }
        delete next[`u:${path}`]
        delete next[`s:${path}`]
        return next
      })
      await reloadExpanded(await refresh())
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(null)
    }
  }

  async function stageHunk(path: string, hunk: string, stagedSection: boolean) {
    const reverse = stagedSection
    const key = `hunk:${path}:${hunk.slice(0, 24)}`
    setBusy(key)
    setError(null)
    try {
      await api(projectPath(projectId, '/git/stage-hunk'), {
        method: 'POST',
        body: JSON.stringify({ path, patch: hunkPatch(path, hunk), reverse }),
      })
      setDiffs((prev) => {
        const next = { ...prev }
        delete next[`u:${path}`]
        delete next[`s:${path}`]
        return next
      })
      await reloadExpanded(await refresh())
    } catch (e) {
      setError(errMsg(e))
    } finally {
      setBusy(null)
    }
  }

  function quoteHunk(path: string, hunk: string) {
    setNotes((prev) => `${prev}${prev.endsWith('\n') || prev === '' ? '' : '\n'}\n${path}\n\`\`\`\n${hunk.trim()}\n\`\`\`\n`)
    setNotesOpen(true)
    setCopied(false)
  }

  // ---- Git tab actions (§2, §4) ----

  // runGit wraps one git-busy action: busy flag + error slot handled once.
  async function runGit<T>(key: string, fn: () => Promise<T>): Promise<T | null> {
    setGitBusy(key)
    setGitError(null)
    try {
      return await fn()
    } catch (e) {
      setGitError(errMsg(e))
      return null
    } finally {
      setGitBusy(null)
    }
  }

  async function doCommit(): Promise<string | null> {
    setGitBusy('commit')
    setGitError(null)
    try {
      const d = await api<{ ok: boolean; commit: string; branch: string }>(
        projectPath(projectId, '/git/commit'),
        { method: 'POST', body: JSON.stringify({ message }) },
      )
      setCommitSha(d.commit)
      setMessage('')
      await reloadExpanded(await refresh())
      return d.commit
    } catch (e) {
      if (e instanceof ApiError && e.status === 409 && String(e.message).includes('identity')) {
        setIdentityOpen(true)
      }
      setGitError(errMsg(e))
      return null
    } finally {
      setGitBusy(null)
    }
  }

  async function saveIdentity() {
    const d = await runGit('identity', () =>
      api<{ name: string; email: string }>(projectPath(projectId, '/git/identity'), {
        method: 'POST',
        body: JSON.stringify({ name: idName, email: idEmail }),
      }),
    )
    if (d) {
      setIdentityOpen(false)
      setGitError(null)
    }
  }

  async function generateMessage() {
    const d = await runGit('gen-msg', () =>
      api<{ subject: string; body: string }>(
        projectPath(projectId, '/git/commit-message'),
        { method: 'POST' },
      ),
    )
    if (d) setMessage(d.body ? `${d.subject}\n\n${d.body}` : d.subject)
  }

  async function doPush() {
    setGitBusy('push')
    setGitError(null)
    setPrState('pushing')
    try {
      await api(projectPath(projectId, '/git/push'), { method: 'POST' })
      setPrState('pushed')
      await refresh()
    } catch (e) {
      setPrState('rejected')
      setGitError(errMsg(e))
    } finally {
      setGitBusy(null)
    }
  }

  async function doPull() {
    const ok = await runGit('pull', () => api(projectPath(projectId, '/git/pull'), { method: 'POST' }))
    if (ok !== null) await refresh()
  }

  // draftDescription asks the model for a PR description of the commit
  // just made. Copy-only: push already shipped the branch, this text is
  // for pasting into the forge.
  async function draftDescription() {
    if (!commitSha) return
    const d = await runGit('gen-pr', () =>
      api<{ title: string; body: string }>(projectPath(projectId, '/git/pr-body'), {
        method: 'POST',
        body: JSON.stringify({ sha: commitSha }),
      }),
    )
    if (d) setPrDesc({ title: d.title, body: d.body })
  }

  // explain fires a working-tree walkthrough (staged + unstaged +
  // untracked — everything uncommitted, not just what will commit).
  // Fire-and-forget: on 202 we jump to the Codemap tab, whose
  // remount-into-run effect opens the thread.
  async function explain() {
    const ok = await runGit('explain', () =>
      api(projectPath(projectId, '/git/explain'), {
        method: 'POST',
        body: JSON.stringify({ mode: 'working-tree' }),
      }),
    )
    if (ok !== null) onSelectView?.('codemap')
  }

  async function openBranchSheet() {
    setBranchOpen(true)
    try {
      const d = await api<GitBranchList>(projectPath(projectId, '/git/branches'))
      setBranches(d)
    } catch (e) {
      setGitError(errMsg(e))
    }
  }

  async function switchBranch(name: string, create = false) {
    setGitBusy('switch')
    setGitError(null)
    try {
      await api(projectPath(projectId, '/git/switch'), {
        method: 'POST',
        body: JSON.stringify({ branch: name, create }),
      })
      setBranchOpen(false)
      setNewBranch('')
      setDiffs({})
      setExpanded({})
      await refresh()
    } catch (e) {
      setGitError(errMsg(e))
    } finally {
      setGitBusy(null)
    }
  }

  async function copyNotes() {
    if (!notes.trim()) return
    if (await copyToClipboard(notes)) setCopied(true)
  }

  const files = status?.files ?? []
  const staged = files.filter((f) => f.staged !== ' ' && f.staged !== '?')
  const unstaged = files.filter((f) => f.unstaged !== ' ' && f.staged !== '?')
  const untracked = files.filter((f) => f.staged === '?' || f.unstaged === '?')

  function renderHunks(path: string, section: 'staged' | 'unstaged') {
    const stagedSection = section === 'staged'
    const fd = diffs[`${stagedSection ? 's' : 'u'}:${path}`]
    if (!fd || fd.loading) {
      return <p className="py-2 text-xs text-muted-foreground">Loading diff…</p>
    }
    if (fd.error) return <p className="py-2 text-xs text-destructive">{fd.error}</p>
    if (fd.binary) return <p className="py-2 text-xs text-muted-foreground">Binary file, not shown.</p>
    const hunks = parseHunks(fd.diff)
    if (hunks.length === 0) return <p className="py-2 text-xs text-muted-foreground">No hunks in this section.</p>
    return (
      <div className="flex flex-col gap-2">
        {fd.truncated && (
          <p className="text-xs text-muted-foreground">Truncated at 300 KB — read the rest in the terminal.</p>
        )}
        {hunks.map((hunk, i) => (
          <div key={i} className="overflow-hidden rounded-lg border">
            <div className="flex flex-wrap items-center gap-2 border-b bg-muted/50 px-2 py-1.5">
              <span className="font-mono text-[11px] text-muted-foreground">
                {hunk.split('\n')[0].slice(0, 48)}
              </span>
              <span className="flex-1" />
              <Button
                size="sm"
                variant="outline"
                className="min-h-[36px]"
                disabled={busy !== null}
                onClick={() => void stageHunk(path, hunk, stagedSection)}
                data-testid={stagedSection ? 'diff-unstage-hunk' : 'diff-stage-hunk'}
              >
                {busy ? '…' : stagedSection ? 'Unstage hunk' : 'Stage hunk'}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                className="min-h-[36px]"
                onClick={() => quoteHunk(path, hunk)}
                data-testid="diff-quote-hunk"
              >
                Quote
              </Button>
            </div>
            <HunkView path={path} hunk={hunk} oldContent={fd.oldContent} newContent={fd.newContent} />
          </div>
        ))}
      </div>
    )
  }

  function renderFile(f: GitFileStatus) {
    const isOpen = !!expanded[f.path]
    const hasUnstaged = f.unstaged !== ' ' || f.staged === '?'
    const hasStaged = f.staged !== ' ' && f.staged !== '?'
    const adds = f.stagedAdd + f.unstagedAdd
    const dels = f.stagedDel + f.unstagedDel
    const ctx = contextByPath[f.path] ?? 3
    return (
      <div key={f.path} className="overflow-hidden rounded-lg border" data-testid="diff-file" data-path={f.path}>
        <button
          className="flex min-h-[48px] w-full items-center gap-2 px-3 py-2 text-left"
          onClick={() => toggle(f.path, f)}
          aria-expanded={isOpen}
        >
          <span className="text-xs text-muted-foreground">{isOpen ? '▾' : '▸'}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-xs">{f.path}</span>
          {f.binary ? (
            <Badge variant="outline">binary</Badge>
          ) : (
            <span className="shrink-0 font-mono text-[11px]">
              <span className="text-emerald-600">+{adds}</span> <span className="text-red-600">-{dels}</span>
            </span>
          )}
        </button>
        {isOpen && (
          <div className="flex flex-col gap-2 border-t p-2">
            <div className="flex flex-wrap items-center gap-2">
              {/* Per-file context expansion (§1.1): 3 → 10 → 30 → full. */}
              {!f.binary && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-h-[36px] px-2 font-mono text-[11px]"
                  onClick={() => bumpContext(f.path)}
                  disabled={ctx === 0}
                  data-testid="diff-context-bump"
                >
                  {ctx === 0 ? 'Context: full' : `Context: ${ctx} ▾`}
                </Button>
              )}
              {hasUnstaged && (
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-[40px] flex-1"
                  disabled={busy !== null}
                  onClick={() => void stageFile(f.path, false)}
                  data-testid="diff-stage-file"
                >
                  {busy ? 'Staging…' : 'Stage file'}
                </Button>
              )}
              {hasStaged && (
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-[40px] flex-1"
                  disabled={busy !== null}
                  onClick={() => void stageFile(f.path, true)}
                  data-testid="diff-unstage-file"
                >
                  {busy ? '…' : 'Unstage file'}
                </Button>
              )}
            </div>
            {f.binary ? (
              <p className="py-2 text-xs text-muted-foreground">Binary file, not shown.</p>
            ) : (
              <>
                {hasStaged && (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs font-medium text-muted-foreground">Staged</p>
                    {renderHunks(f.path, 'staged')}
                  </div>
                )}
                {hasUnstaged && (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs font-medium text-muted-foreground">
                      {hasStaged ? 'Unstaged' : f.staged === '?' ? 'Untracked' : 'Changes'}
                    </p>
                    {renderHunks(f.path, 'unstaged')}
                  </div>
                )}
              </>
            )}
          </div>
        )}
      </div>
    )
  }

  const upstream = status?.upstream ?? null
  const treeDirty = files.length > 0
  const ahead = upstream?.ahead ?? 0
  const hasUpstream = upstream !== null
  // Push needs something to send: unborn HEAD (zero commits) has
  // nothing, and a clean tree already in sync is a no-op. First push
  // (no upstream yet) stays enabled — it records tracking via -u.
  const pushDisabled = status?.unborn
    ? 'Commit first — this repo has no commits yet.'
    : !treeDirty && hasUpstream && ahead === 0
      ? 'Nothing to push — the branch is up to date.'
      : null

  return (
    <Card className="flex h-full w-full flex-col shadow-sm" data-testid="diff-tab">
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          <CardTitle className="text-base">Git</CardTitle>
          <button
            onClick={() => void openBranchSheet()}
            className="min-h-[36px]"
            data-testid="git-branch-badge"
            title="Switch branch"
          >
            <Badge variant="outline" className="font-mono font-normal">
              {status?.branch || '—'} {hasUpstream && ahead > 0 ? `↑${ahead}` : ''}
            </Badge>
          </button>
          {files.length > 0 && (
            <Badge variant="secondary" className="font-normal">
              {files.length} {files.length === 1 ? 'file' : 'files'}
            </Badge>
          )}
          <span className="flex-1" />
          <Button size="sm" variant="outline" onClick={() => void refresh()} data-testid="diff-refresh">
            ↻ Refresh
          </Button>
        </div>
        <CardDescription>Review what the harness changed, then commit and push.</CardDescription>
      </CardHeader>
      <Separator />
      <CardContent className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pt-3">
        {error && <p className="text-xs text-destructive" data-testid="diff-error">{error}</p>}
        {gitError && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-2" data-testid="git-error">
            <p className="min-w-0 flex-1 text-xs break-all text-destructive">{gitError}</p>
            <Button
              size="sm"
              variant="ghost"
              className="min-h-[36px] shrink-0"
              onClick={() => void copyToClipboard(gitError)}
              data-testid="git-error-copy"
            >
              Copy
            </Button>
          </div>
        )}
        {/* Message box (§2): always visible at the top of the content. */}
        {!status?.notRepo && (
          <div className="flex flex-col gap-1.5 rounded-lg border p-2" data-testid="git-commit-section">
            <div className="flex items-center gap-2">
              <span className="text-xs font-medium text-muted-foreground">Commit</span>
              <span className="flex-1" />
              <Button
                size="sm"
                variant="ghost"
                className="min-h-[36px]"
                disabled={gitBusy !== null || !treeDirty}
                onClick={() => void generateMessage()}
                data-testid="git-generate-message"
              >
                {gitBusy === 'gen-msg' ? '✨ Generating…' : '✨ Generate'}
              </Button>
            </div>
            <textarea
              className="min-h-12 w-full rounded-md border bg-background p-2 font-mono text-xs"
              placeholder="Commit message (multi-line ok)"
              value={message}
              onChange={(e) => setMessage(e.target.value)}
              data-testid="git-message"
            />
            {identityOpen && (
              <div className="flex flex-col gap-2 rounded-md border bg-muted/30 p-2" data-testid="git-identity-form">
                <p className="text-xs text-muted-foreground">Set git identity for this repo (required to commit):</p>
                <Input placeholder="Name" value={idName} onChange={(e) => setIdName(e.target.value)} data-testid="git-identity-name" />
                <Input placeholder="Email" value={idEmail} onChange={(e) => setIdEmail(e.target.value)} data-testid="git-identity-email" />
                <Button
                  size="sm"
                  className="min-h-[44px]"
                  disabled={gitBusy !== null || !idName.trim() || !idEmail.trim()}
                  onClick={() => void saveIdentity()}
                  data-testid="git-identity-save"
                >
                  Save identity
                </Button>
              </div>
            )}
          </div>
        )}
        {status === null ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : status.notRepo ? (
          <div className="grid place-items-center rounded-lg border border-dashed bg-muted/30 p-8 text-center" data-testid="diff-empty">
            <div className="flex flex-col items-center gap-2">
              <span className="text-2xl">○</span>
              <p className="text-sm font-medium">Not a git repo</p>
              <p className="text-xs text-muted-foreground">Init one in the terminal to review diffs here.</p>
            </div>
          </div>
        ) : (
          <>
            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                variant="ghost"
                className="min-h-[36px]"
                disabled={gitBusy !== null || !treeDirty}
                onClick={() => void explain()}
                data-testid="git-explain-tree"
              >
                {gitBusy === 'explain' ? 'Explaining…' : 'Explain in Codemap'}
              </Button>
              {files.length > 0 && (
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-h-[36px]"
                  onClick={collapseAll}
                  data-testid="git-collapse-all"
                >
                  Collapse all
                </Button>
              )}
            </div>
            {files.length === 0 ? (
              <div className="grid place-items-center rounded-lg border border-dashed bg-muted/30 p-8 text-center" data-testid="diff-empty">
                <div className="flex flex-col items-center gap-2">
                  <span className="text-2xl">○</span>
                  <p className="text-sm font-medium">No changes</p>
                  <p className="text-xs text-muted-foreground">Working tree is clean.</p>
                </div>
              </div>
            ) : (
              // min-h-36 floors the list: on short screens the fixed
              // message/actions blocks must not squeeze it to zero.
              <div className="flex min-h-36 flex-1 flex-col gap-2 overflow-auto" data-testid="diff-file-list">
                {staged.length > 0 && (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs font-medium text-muted-foreground">Staged ({staged.length})</p>
                    {staged.map(renderFile)}
                  </div>
                )}
                {unstaged.length > 0 && (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs font-medium text-muted-foreground">Unstaged ({unstaged.length})</p>
                    {unstaged.map(renderFile)}
                  </div>
                )}
                {untracked.length > 0 && (
                  <div className="flex flex-col gap-2">
                    <p className="text-xs font-medium text-muted-foreground">Untracked ({untracked.length})</p>
                    {untracked.map(renderFile)}
                  </div>
                )}
              </div>
            )}
          </>
        )}
        {/* Commit and Push are separate buttons: commit lands locally,
            push ships the branch (first push records tracking via -u).
            No combined states — a failed push leaves the sha chip and a
            push-only retry, never a duplicate commit. */}
        {!status?.notRepo && (
          <div className="flex flex-col gap-1.5 rounded-lg border p-2" data-testid="git-actions">
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                className="min-h-[44px] flex-1"
                disabled={gitBusy !== null || !message.trim() || !treeDirty}
                onClick={() => void doCommit()}
                data-testid="git-commit"
              >
                {gitBusy === 'commit' ? 'Committing…' : 'Commit'}
              </Button>
              <Button
                size="sm"
                variant="outline"
                className="min-h-[44px] flex-1"
                disabled={gitBusy !== null || pushDisabled !== null}
                title={pushDisabled ?? undefined}
                onClick={() => void doPush()}
                data-testid="git-push"
              >
                {gitBusy === 'push' ? 'Pushing…' : 'Push'}
              </Button>
              {hasUpstream && upstream.behind > 0 && (
                <Button size="sm" variant="outline" className="min-h-[44px]" disabled={gitBusy !== null} onClick={() => void doPull()} data-testid="git-pull">
                  {gitBusy === 'pull' ? 'Pulling…' : `Pull (↓${upstream.behind})`}
                </Button>
              )}
            </div>
            {pushDisabled !== null && gitBusy === null && (
              <p className="text-[11px] text-muted-foreground" data-testid="git-push-hint">{pushDisabled}</p>
            )}
          </div>
        )}
        {/* Ship status: the commit sha, push state, and the AI-drafted PR
            description (copy-only — push already shipped the branch). Kept
            separate from the actions above so success stays visible after
            the poll refreshes the tree clean. */}
        {!status?.notRepo && (commitSha !== null || prState !== 'idle' || prDesc !== null) && (
          <div className="flex flex-col gap-1.5 rounded-lg border p-2" data-testid="git-ship">
            {commitSha && (
              <div className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="git-commit-sha">
                <Badge variant="secondary" className="font-mono">{commitSha}</Badge> committed
              </div>
            )}
            {prState !== 'idle' && (
              <p className="text-xs text-muted-foreground" data-testid="git-push-state">
                {prState === 'pushing' ? 'Pushing…' : prState === 'pushed' ? 'Pushed ✓' : 'Push rejected ✗ — see error above'}
              </p>
            )}
            {commitSha && !prDesc && (
              <div className="flex items-center gap-2">
                <span className="flex-1" />
                <Button size="sm" variant="ghost" className="min-h-[36px]" disabled={gitBusy !== null} onClick={() => void draftDescription()} data-testid="git-draft-desc">
                  {gitBusy === 'gen-pr' ? '✨ Drafting…' : '✨ Draft PR description'}
                </Button>
              </div>
            )}
            {prDesc && (
              <div className="flex flex-col gap-1.5 rounded-md border bg-muted/20 p-2" data-testid="git-pr-desc">
                <p className="text-xs font-medium break-all">{prDesc.title}</p>
                <p className="text-xs whitespace-pre-wrap break-all text-muted-foreground">{prDesc.body}</p>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-[36px] self-start"
                  onClick={() => void copyToClipboard(`${prDesc.title}\n\n${prDesc.body}`)}
                  data-testid="git-pr-desc-copy"
                >
                  Copy description
                </Button>
              </div>
            )}
          </div>
        )}

        {/* ---- Branch sheet (§5) ---- */}
        {branchOpen && (
          <BranchSheet
            branches={branches}
            gitBusy={gitBusy}
            newBranch={newBranch}
            onNewBranch={setNewBranch}
            hasUpstream={hasUpstream}
            ahead={ahead}
            onClose={() => setBranchOpen(false)}
            onSwitch={(name, create) => void switchBranch(name, create)}
          />
        )}

        <div className="rounded-lg border" data-testid="diff-notepad">
          <button
            className="flex min-h-[44px] w-full items-center gap-2 px-3 py-2 text-left text-sm"
            onClick={() => setNotesOpen((v) => !v)}
            data-testid="diff-notepad-toggle"
          >
            <span className="text-xs text-muted-foreground">{notesOpen ? '▾' : '▸'}</span>
            <span className="font-medium">Ask-AI notes</span>
            {notes.trim() && <Badge variant="secondary">{notes.split('\n').length} lines</Badge>}
            <span className="flex-1" />
            <span className="text-xs text-muted-foreground">copy only</span>
          </button>
          {notesOpen && (
            <div className="flex flex-col gap-2 border-t p-2 pb-[env(safe-area-inset-bottom)]">
              <textarea
                className="min-h-24 w-full rounded-md border bg-background p-2 font-mono text-xs"
                placeholder="Quote hunks above, add questions or critique, then copy into your harness."
                value={notes}
                onChange={(e) => {
                  setNotes(e.target.value)
                  setCopied(false)
                }}
                data-testid="diff-notes"
              />
              <div className="flex gap-2">
                <Button
                  size="sm"
                  className="min-h-[44px] flex-1"
                  disabled={!notes.trim()}
                  onClick={() => void copyNotes()}
                  data-testid="diff-copy"
                >
                  {copied ? 'Copied ✓' : 'Copy notes'}
                </Button>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-[44px]"
                  disabled={!notes.trim()}
                  onClick={() => {
                    setNotes('')
                    setCopied(false)
                  }}
                  data-testid="diff-clear-notes"
                >
                  Clear
                </Button>
              </div>
            </div>
          )}
        </div>
      </CardContent>
    </Card>
  )
}
