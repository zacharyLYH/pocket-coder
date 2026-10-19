import { memo, useCallback, useEffect, useMemo, useState } from 'react'
import { DiffModeEnum, DiffView } from '@git-diff-view/react'
import '@git-diff-view/react/styles/diff-view.css'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Input } from '@/components/ui/input'
import { Separator } from '@/components/ui/separator'
import { Textarea } from '@/components/ui/textarea'
import { api, ApiError, errMsg, projectPath } from '@/lib/api'
import { copyToClipboard } from '@/lib/clipboard'
import { resolvePrimaryAction } from '@/lib/git-primary-action'
import { BranchSheet } from '@/components/terminal/BranchSheet'
import { QuoteDialog } from '@/components/terminal/QuoteDialog'
import { EmptyState } from '@/components/EmptyState'
import { GitOpsPrompt } from '@/components/terminal/GitOpsPrompt'
import { hunkPatch, parseHunks } from '@/lib/diffHunks'
import type { GitBranchList, GitDiffResponse, GitFileStatus, GitStatusResponse } from '@/lib/types'

// Sticky section labels: the file list scrolls, so each "Staged / Unstaged /
// Untracked" label stays pinned while its own files scroll past, keeping the
// section a file belongs to visible.
const sectionHeader =
  'sticky top-0 z-10 -mx-0.5 rounded bg-background/95 px-1.5 py-1 text-xs font-medium text-muted-foreground backdrop-blur-sm'

// Context levels for the per-file progressive expansion.
const CONTEXT_LEVELS = [3, 10, 30, 0] as const // 0 = full
const DEFAULT_CONTEXT = 3
const contextParam = (n: number) => (n === 0 ? 'full' : String(n))


const hasStaged = (f: GitFileStatus) => f.staged !== ' ' && f.staged !== '?'
const hasUnstaged = (f: GitFileStatus) => f.staged === '?' || f.unstaged !== ' '
const isUntracked = (f: GitFileStatus) => f.staged === '?' || f.unstaged === '?'

type FileDiff = {
  diff: string
  truncated: boolean
  oldContent: string
  newContent: string
  binary: boolean
  loading: boolean
  error: string | null
}

const emptyFileDiff: FileDiff = {
  diff: '',
  truncated: false,
  oldContent: '',
  newContent: '',
  binary: false,
  loading: false,
  error: null,
}

// One row's UI state: expansion, context level, and both cached diff sides.
type FileUi = {
  open: boolean
  context: number
  staged: FileDiff | null
  unstaged: FileDiff | null
}

const emptyFileUi: FileUi = { open: false, context: 3, staged: null, unstaged: null }

type Identity = { open: boolean; name: string; email: string }
type Ship = { sha: string | null; state: 'idle' | 'pushing' | 'pushed' | 'rejected'; pr: { title: string; body: string } | null }
type BranchUi = { open: boolean; list: GitBranchList | null; name: string }

// One request state for the tab: last good status, last failure, and the
// action currently in flight. They move together — a mutation clears the
// error and the busy flag — so one object beats three useStates that have to
// be kept in sync by hand.
type Repo = { data: GitStatusResponse | null; error: string | null; busy: string | null }

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
  const [repo, setRepo] = useState<Repo>({ data: null, error: null, busy: null })
  const { data: status, error, busy } = repo
  const [fileUi, setFileUi] = useState<Record<string, FileUi>>({})
  const [quoteTarget, setQuoteTarget] = useState<{ path: string; hunk: string } | null>(null)
  const [notepad, setNotepad] = useState<{ text: string; copied: boolean }>(() => {
    let text = ''
    try {
      text = localStorage.getItem(`pcoder-diff-notes-${projectId}`) ?? ''
    } catch {
      // Private browsing: notes just do not persist.
    }
    return { text, copied: false }
  })
  const { text: notes, copied } = notepad
  const [draft, setDraft] = useState<{ message: string; identity: Identity }>({
    message: '',
    identity: { open: false, name: '', email: '' },
  })
  const { message, identity } = draft
  const [ship, setShip] = useState<Ship>({ sha: null, state: 'idle', pr: null })
  const [branch, setBranch] = useState<BranchUi>({ open: false, list: null, name: '' })

  const patchRepo = useCallback((p: Partial<Repo>) => setRepo((prev) => ({ ...prev, ...p })), [])
  const patchNotepad = (p: Partial<{ text: string; copied: boolean }>) =>
    setNotepad((prev) => ({ ...prev, ...p }))
  const patchDraft = (p: Partial<{ message: string; identity: Identity }>) =>
    setDraft((prev) => ({ ...prev, ...p }))

  // post is the only write verb this tab uses: it keeps URL + JSON plumbing
  // in one place so every action below reads as the endpoint it calls.
  const post = useCallback(
    <T,>(path: string, body?: unknown): Promise<T> =>
      api<T>(projectPath(projectId, path), {
        method: 'POST',
        body: body === undefined ? undefined : JSON.stringify(body),
      }),
    [projectId],
  )

  const refresh = useCallback(async (): Promise<GitStatusResponse | null> => {
    try {
      const d = await api<GitStatusResponse>(projectPath(projectId, '/git/status'))
      patchRepo({ data: d, error: null })
      return d
    } catch (e) {
      // Keep the last-known list: a transient probe failure should not
      // blink the file list away mid-read.
      patchRepo({ error: errMsg(e) })
      return null
    }
  }, [projectId, patchRepo])

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

  function patchFile(path: string, patch: Partial<FileUi>) {
    setFileUi((prev) => ({ ...prev, [path]: { ...emptyFileUi, ...prev[path], ...patch } }))
  }

  function mapFiles(fn: (ui: FileUi) => FileUi) {
    setFileUi((prev) => Object.fromEntries(Object.entries(prev).map(([path, ui]) => [path, fn(ui)])))
  }

  // loadDiff fills one section of one file. The section name is the only
  // thing that differs between staged and unstaged, so it indexes the state
  // instead of branching at every write.
  async function loadDiff(path: string, staged: boolean, contextLevel?: number) {
    const section = staged ? 'staged' : 'unstaged'
    const ctx = contextLevel ?? fileUi[path]?.context ?? DEFAULT_CONTEXT
    patchFile(path, { [section]: { ...emptyFileDiff, loading: true } })
    try {
      const d = await api<GitDiffResponse>(
        projectPath(
          projectId,
          `/git/diff?path=${encodeURIComponent(path)}&staged=${staged}&context=${contextParam(ctx)}`,
        ),
      )
      patchFile(path, {
        [section]: {
          diff: d.diff,
          truncated: d.truncated,
          oldContent: d.oldContent ?? '',
          newContent: d.newContent ?? '',
          binary: d.binary ?? false,
          loading: false,
          error: null,
        },
      })
    } catch (e) {
      patchFile(path, { [section]: { ...emptyFileDiff, error: errMsg(e) } })
    }
  }

  // clearDiffs drops a file's cached hunks after a mutation: the next render
  // refetches them instead of showing pre-mutation lines.
  function clearDiffs(path: string) {
    setFileUi((prev) => {
      const ui = prev[path]
      if (!ui) return prev
      return { ...prev, [path]: { ...ui, staged: null, unstaged: null } }
    })
  }

  function toggle(path: string, f: GitFileStatus) {
    const open = !fileUi[path]?.open
    patchFile(path, { open })
    if (!open) return
    const ui = fileUi[path]
    if (hasUnstaged(f) && !ui?.unstaged) void loadDiff(path, false)
    if (hasStaged(f) && !ui?.staged) void loadDiff(path, true)
  }

  // bumpContext moves one file through 3 → 10 → 30 → full and refetches.
  function bumpContext(path: string) {
    const cur = fileUi[path]?.context ?? DEFAULT_CONTEXT
    const next = CONTEXT_LEVELS.find((n) => n > cur) ?? cur
    patchFile(path, { context: next })
    const f = status?.files.find((x) => x.path === path)
    if (!f) return
    if (hasUnstaged(f)) void loadDiff(path, false, next)
    if (hasStaged(f)) void loadDiff(path, true, next)
  }

  // collapseAll resets every file's expansion + context (ephemeral).
  function collapseAll() {
    mapFiles((ui) => ({ ...ui, open: false, context: DEFAULT_CONTEXT }))
  }

  // reloadExpanded refetches both diff sections for every expanded file
  // still present after a stage/unstage mutation. Without this an expanded
  // file keeps showing its pre-mutation hunks (or a stale loading state).
  async function reloadExpanded(fresh: GitStatusResponse | null) {
    if (!fresh) return
    const byPath = new Map(fresh.files.map((f) => [f.path, f]))
    await Promise.all(
      Object.entries(fileUi).flatMap(([path, ui]) => {
        const f = byPath.get(path)
        if (!f || !ui.open) return []
        const jobs: Promise<void>[] = []
        if (hasUnstaged(f)) jobs.push(loadDiff(path, false))
        if (hasStaged(f)) jobs.push(loadDiff(path, true))
        return jobs
      }),
    )
  }

  // stageMut posts one stage/unstage mutation, then clears the cached
  // diffs and reloads what is still expanded. stageFile and stageHunk
  // differ only in endpoint + payload.
  async function stageMut(path: string, endpoint: string, payload: Record<string, unknown>, busyKey: string) {
    if ((await run(busyKey, () => post(endpoint, payload))) === null) return
    clearDiffs(path)
    await reloadExpanded(await refresh())
  }

  async function stageFile(path: string, unstage: boolean) {
    await stageMut(path, `/git/${unstage ? 'unstage' : 'stage'}`, { path }, `file:${path}:${unstage ? 'u' : 's'}`)
  }

  // stageAll is the primary CTA's bulk step: one POST per stageable file,
  // then a single refresh. Conflicts are never staged.
  async function stageAll() {
    const targets = files.filter((f) => !f.conflict && hasUnstaged(f))
    if (targets.length === 0) return
    await run('stage-all', async () => {
      for (const f of targets) await post('/git/stage', { path: f.path })
    })
    mapFiles((ui) => ({ ...ui, open: false, staged: null, unstaged: null }))
    await refresh()
  }

  async function stageHunk(path: string, hunk: string, stagedSection: boolean) {
    await stageMut(path, '/git/stage-hunk', { path, patch: hunkPatch(path, hunk), reverse: stagedSection }, `hunk:${path}:${hunk.slice(0, 24)}`)
  }

  // addQuote appends one review block to the notepad. The hunk is quoted as
  // a diff fence (not a bare code fence) so the harness sees what changed.
  function addQuote(comment: string) {
    if (!quoteTarget) return
    const block = [quoteTarget.path, '```diff', quoteTarget.hunk.trim(), '```']
    if (comment) block.push('', comment)
    patchNotepad({ text: notes.trim() ? `${notes.trimEnd()}\n\n${block.join('\n')}` : block.join('\n'), copied: false })
    setQuoteTarget(null)
  }

  // run wraps one action: busy flag + error slot handled once. onErr maps
  // the error before it lands in the slot (commit uses it to pop the
  // identity form on a 409); the resolved value returns, or null on failure.
  async function run<T>(key: string, fn: () => Promise<T>, onErr?: (e: unknown) => void): Promise<T | null> {
    patchRepo({ busy: key, error: null })
    try {
      return await fn()
    } catch (e) {
      onErr?.(e)
      patchRepo({ error: errMsg(e) })
      return null
    } finally {
      patchRepo({ busy: null })
    }
  }

  async function doCommit(): Promise<string | null> {
    const d = await run(
      'commit',
      () => post<{ ok: boolean; commit: string; branch: string }>('/git/commit', { message }),
      (e) => {
        if (e instanceof ApiError && e.status === 409 && String(e.message).includes('identity')) {
          patchDraft({ identity: { ...identity, open: true } })
        }
      },
    )
    if (!d) return null
    setShip((prev) => ({ ...prev, sha: d.commit }))
    patchDraft({ message: '' })
    await reloadExpanded(await refresh())
    return d.commit
  }

  async function saveIdentity() {
    const d = await run('identity', () =>
      post<{ name: string; email: string }>('/git/identity', { name: identity.name, email: identity.email }),
    )
    if (d) patchDraft({ identity: { ...identity, open: false } })
  }

  async function generateMessage() {
    const d = await run('gen-msg', () => post<{ subject: string; body: string }>('/git/commit-message'))
    if (d) patchDraft({ message: d.body ? `${d.subject}\n\n${d.body}` : d.subject })
  }

  async function doPush() {
    setShip((prev) => ({ ...prev, state: 'pushing' }))
    const ok = await run('push', () => post('/git/push'))
    setShip((prev) => ({ ...prev, state: ok !== null ? 'pushed' : 'rejected' }))
    if (ok !== null) await refresh()
  }

  async function doPull() {
    if ((await run('pull', () => post('/git/pull'))) !== null) await refresh()
  }

  // draftDescription asks the model for a PR description of the commit
  // just made. Copy-only: push already shipped the branch, this text is
  // for pasting into the forge.
  async function draftDescription() {
    if (!ship.sha) return
    const d = await run('gen-pr', () => post<{ title: string; body: string }>('/git/pr-body', { sha: ship.sha }))
    if (d) setShip((prev) => ({ ...prev, pr: { title: d.title, body: d.body } }))
  }

  // explain fires a working-tree walkthrough (staged + unstaged +
  // untracked — everything uncommitted, not just what will commit).
  // Fire-and-forget: on 202 we jump to the Codemap tab, whose
  // remount-into-run effect opens the thread.
  async function explain() {
    if ((await run('explain', () => post('/git/explain', { mode: 'working-tree' }))) !== null) {
      onSelectView?.('codemap')
    }
  }

  async function openBranchSheet() {
    setBranch((prev) => ({ ...prev, open: true }))
    try {
      const list = await api<GitBranchList>(projectPath(projectId, '/git/branches'))
      setBranch((prev) => ({ ...prev, list }))
    } catch (e) {
      patchRepo({ error: errMsg(e) })
    }
  }

  async function switchBranch(name: string, create = false) {
    if ((await run('switch', () => post('/git/switch', { branch: name, create }))) === null) return
    setBranch((prev) => ({ ...prev, open: false, name: '' }))
    setFileUi({})
    await refresh()
  }

  async function copyNotes() {
    if (notes.trim() && (await copyToClipboard(notes))) patchNotepad({ copied: true })
  }

  const files = status?.files ?? []
  // Conflicts render first and are excluded from the normal sections: they
  // need resolution, not staging. The backend marks them from porcelain's
  // unmerged XY pairs (UU, AA, DD, AU, UA, DU, UD).
  const conflicts = files.filter((f) => f.conflict)
  const staged = files.filter((f) => !f.conflict && hasStaged(f))
  const unstaged = files.filter((f) => !f.conflict && !isUntracked(f) && f.unstaged !== ' ')
  const untracked = files.filter((f) => !f.conflict && isUntracked(f))
  const stageable = files.filter((f) => !f.conflict && hasUnstaged(f))

  function renderHunks(path: string, section: 'staged' | 'unstaged') {
    const stagedSection = section === 'staged'
    const fd = fileUi[path]?.[section]
    if (!fd || fd.loading) {
      return <p className="py-2 text-xs text-muted-foreground">Loading diff…</p>
    }
    if (fd.error) return <p className="py-2 text-xs text-destructive">{fd.error}</p>
    if (fd.binary) return <p className="py-2 text-xs text-muted-foreground">Binary file, not shown.</p>
    const hunks = parseHunks(fd.diff)
    if (hunks.length === 0) return <p className="py-2 text-xs text-muted-foreground">No hunks in this section.</p>
    return (
      <div className="flex flex-col gap-1.5">
        {fd.truncated && (
          <p className="text-xs text-muted-foreground">Truncated at 300 KB — read the rest in the terminal.</p>
        )}
        {hunks.map((hunk, i) => (
          <div key={i} className="overflow-hidden rounded-lg border bg-background shadow-xs" data-testid="diff-hunk">
            <div className="flex flex-wrap items-center gap-2 border-b bg-muted/50 px-2 py-1.5">
              <span className="shrink-0 rounded bg-background px-1 py-0.5 text-[10px] font-medium text-muted-foreground">
                Hunk {i + 1}/{hunks.length}
              </span>
              <span className="min-w-0 truncate font-mono text-[11px] text-muted-foreground">
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
                onClick={() => setQuoteTarget({ path, hunk })}
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

  // diffSection is the shell every diff side shares: a label plus the hunks
  // for that side. Staged, unstaged, and untracked only differ in the label.
  function diffSection(path: string, section: 'staged' | 'unstaged', label: string, show: boolean) {
    if (!show) return null
    return (
      <div className="flex flex-col gap-1.5">
        <p className="text-xs font-medium text-muted-foreground">{label}</p>
        {renderHunks(path, section)}
      </div>
    )
  }

  function stageButton(path: string, unstage: boolean) {
    return (
      <Button
        size="xs"
        variant="outline"
        className="min-h-[32px] shrink-0"
        disabled={busy !== null}
        onClick={() => void stageFile(path, unstage)}
        title={unstage ? 'Unstage file' : 'Stage file'}
        data-testid={unstage ? 'diff-unstage-file' : 'diff-stage-file'}
      >
        {busy ? '…' : unstage ? 'Unstage' : 'Stage'}
      </Button>
    )
  }

  function fileSection(label: string, list: GitFileStatus[]) {
    if (list.length === 0) return null
    return (
      <div className="flex flex-col gap-2">
        <p className={sectionHeader}>{label} ({list.length})</p>
        {list.map(renderFile)}
      </div>
    )
  }

  function renderFile(f: GitFileStatus) {
    const ui = fileUi[f.path]
    const isOpen = !!ui?.open
    const canStage = hasUnstaged(f)
    const canUnstage = hasStaged(f)
    const adds = f.stagedAdd + f.unstagedAdd
    const dels = f.stagedDel + f.unstagedDel
    const ctx = ui?.context ?? 3
    return (
      <div key={f.path} className="overflow-hidden rounded-lg border bg-card shadow-sm" data-testid="diff-file" data-path={f.path}>
        <div className="flex min-h-[48px] items-center gap-1.5 px-3 py-2">
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-auto min-h-[48px] min-w-0 flex-1 justify-start gap-2 px-2 py-2 text-left font-normal"
            onClick={() => toggle(f.path, f)}
            aria-expanded={isOpen}
            data-testid="diff-file-toggle"
          >
            <span className="text-xs text-muted-foreground">{isOpen ? '▾' : '▸'}</span>
            <span className="min-w-0 flex-1">
              <span className="block truncate font-mono text-xs">{f.path}</span>
              {f.conflict && <span className="block text-[11px] text-destructive">Unresolved conflict</span>}
            </span>
            {f.conflict && <Badge variant="destructive" className="shrink-0" data-testid="diff-conflict-badge">Conflict</Badge>}
            {f.binary ? (
              <Badge variant="outline">binary</Badge>
            ) : (
              <span className="shrink-0 font-mono text-[11px]">
                <span className="text-emerald-600">+{adds}</span> <span className="text-red-600">-{dels}</span>
              </span>
            )}
          </Button>
          {canStage && !f.conflict && stageButton(f.path, false)}
          {canUnstage && !f.conflict && stageButton(f.path, true)}
        </div>
        {isOpen && (
          <div className="flex flex-col gap-1.5 border-t p-2">
            {(!f.binary || f.conflict) && (
              <div className="flex flex-wrap items-center gap-2">
                {/* Per-file context expansion: 3 → 10 → 30 → full. */}
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
                {f.conflict && (
                  <p className="text-[11px] text-destructive">
                    Resolve the markers in the terminal, then stage the file there or press Stage all.
                  </p>
                )}
              </div>
            )}
            {f.binary ? (
              <p className="py-2 text-xs text-muted-foreground">Binary file, not shown.</p>
            ) : (
              <>
                {diffSection(f.path, 'staged', 'Staged', canUnstage && !f.conflict)}
                {diffSection(
                  f.path,
                  'unstaged',
                  canUnstage ? 'Unstaged' : f.staged === '?' ? 'Untracked' : f.conflict ? 'Conflict' : 'Changes',
                  canStage,
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
  const behind = upstream?.behind ?? 0
  const hasUpstream = upstream !== null
  // One CTA: its label always says what pressing it does.
  const action = resolvePrimaryAction({
    stagedCount: staged.length,
    unstagedCount: stageable.length,
    hasMessage: message.trim() !== '',
    ahead,
    behind,
    unborn: !!status?.unborn,
    hasUnresolvedConflicts: conflicts.length > 0,
    conflictOperation: status?.conflictOperation,
    busy: busy !== null,
  })
  const BUSY_LABEL: Record<string, string> = {
    commit: 'Committing…',
    push: 'Pushing…',
    pull: 'Pulling…',
    'stage-all': 'Staging…',
  }
  const busyLabel = busy ? BUSY_LABEL[busy] ?? null : null

  async function runPrimary() {
    switch (action.kind) {
      case 'stage-all':
        await stageAll()
        break
      case 'commit':
        await doCommit()
        break
      case 'commit-push': {
        const sha = await doCommit()
        if (sha) await doPush()
        break
      }
      case 'push':
        await doPush()
        break
      case 'pull':
        await doPull()
        break
    }
  }

  return (
    <Card className="flex h-full w-full flex-col gap-2 py-3 shadow-sm" data-testid="diff-tab">
      <CardHeader className="px-3 pt-3 pb-2">
        <div className="flex items-center gap-2">
          <CardTitle className="text-base">Git</CardTitle>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            onClick={() => void openBranchSheet()}
            className="h-auto min-h-[36px] rounded-full px-1 py-0.5"
            data-testid="git-branch-badge"
            title="Switch branch"
          >
            <Badge variant="outline" className="font-mono font-normal">
              {status?.branch || '—'} {hasUpstream && ahead > 0 ? `↑${ahead}` : ''}
            </Badge>
          </Button>
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
        <CardDescription>Review what the harness changed, then commit and push. Pushes go straight to the current branch — PRs and syncs run in your terminal AI (below).</CardDescription>
      </CardHeader>
      <Separator />
      <CardContent className="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto px-2 pt-2 pb-2">
        {error && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/10 p-2" data-testid="git-error">
            <p className="min-w-0 flex-1 text-xs break-all text-destructive" data-testid="diff-error">{error}</p>
            <Button
              size="sm"
              variant="ghost"
              className="min-h-[36px] shrink-0"
              onClick={() => void copyToClipboard(error)}
              data-testid="git-error-copy"
            >
              Copy
            </Button>
          </div>
        )}
        {status === null ? (
          <p className="text-xs text-muted-foreground">Loading…</p>
        ) : status.notRepo ? (
          <EmptyState testid="diff-empty" title="Not a git repo" hint="Init one in the terminal to review diffs here." />
        ) : (
          <>
            <div className="flex items-center justify-end gap-2">
              <Button
                size="sm"
                variant="ghost"
                className="min-h-[36px]"
                disabled={busy !== null || !treeDirty}
                onClick={() => void explain()}
                data-testid="git-explain-tree"
              >
                {busy === 'explain' ? 'Explaining…' : 'Explain in Codemap'}
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
              <EmptyState testid="diff-empty" title="No changes" hint="Working tree is clean." />
            ) : (
              // The diff is the point of this tab: floor the list at most of
              // the viewport so the sticky message/actions block below can
              // never squeeze the changes into a postage stamp.
              <div className="flex min-h-[50dvh] flex-1 flex-col gap-2 overflow-auto rounded-lg bg-muted/30 p-2" data-testid="diff-file-list">
                {conflicts.length > 0 && (
                  <div className="flex flex-col gap-2 rounded-lg border border-destructive/40 bg-destructive/5 p-2" data-testid="diff-conflicts">
                    <p className="text-xs font-medium text-destructive">Conflicts ({conflicts.length})</p>
                    <p className="text-[11px] text-muted-foreground">
                      Resolve them in the terminal, then commit.{' '}
                      {status?.conflictOperation ? `A ${status.conflictOperation} is in progress — ` : ''}
                      {status?.conflictOperation === 'rebase' ? '`git rebase --abort` cancels it.' : '`git merge --abort` cancels it.'}
                    </p>
                    {conflicts.map(renderFile)}
                  </div>
                )}
                {fileSection('Staged', staged)}
                {fileSection('Unstaged', unstaged)}
                {fileSection('Untracked', untracked)}
              </div>
            )}
          </>
        )}
        {/* Sticky action deck: commit on the left, Ask-AI notes on the right.
            Both halves stay visible, so quoting a hunk and commenting never
            makes the user scroll to a separate drawer. One CTA whose label
            always says what it does — Stage all → Commit → Commit & Push →
            Push/Pull — instead of static Commit + Push buttons the user has
            to reason about. */}
        {!status?.notRepo && (
          <div
            className="grid grid-cols-2 gap-1.5 rounded-lg border bg-card/95 p-1.5 pb-[max(0.375rem,env(safe-area-inset-bottom))] shadow-md"
            data-testid="git-actions"
          >
            <div className="flex min-w-0 flex-col gap-1" data-testid="git-commit-section">
              <div className="flex items-center gap-1.5">
                <span className="text-xs font-medium text-muted-foreground">Commit</span>
                <span className="flex-1" />
                <Button
                  size="sm"
                  variant="ghost"
                  className="min-h-[32px] shrink-0 px-2 text-xs"
                  disabled={busy !== null || !treeDirty}
                  onClick={() => void generateMessage()}
                  data-testid="git-generate-message"
                >
                  {busy === 'gen-msg' ? '✨ …' : '✨ Generate'}
                </Button>
              </div>
              <Textarea
                className="min-h-40 flex-1 resize-y font-mono text-xs"
                placeholder="Commit message (multi-line ok)"
                rows={6}
                value={message}
                onChange={(e) => patchDraft({ message: e.target.value })}
                data-testid="git-message"
              />
              {identity.open && (
                <div className="flex flex-col gap-2 rounded-md border bg-muted/30 p-2" data-testid="git-identity-form">
                  <p className="text-xs text-muted-foreground">Set git identity for this repo (required to commit):</p>
                  <Input placeholder="Name" value={identity.name} onChange={(e) => patchDraft({ identity: { ...identity, name: e.target.value } })} data-testid="git-identity-name" />
                  <Input placeholder="Email" value={identity.email} onChange={(e) => patchDraft({ identity: { ...identity, email: e.target.value } })} data-testid="git-identity-email" />
                  <Button
                    size="sm"
                    className="min-h-[44px]"
                    disabled={busy !== null || !identity.name.trim() || !identity.email.trim()}
                    onClick={() => void saveIdentity()}
                    data-testid="git-identity-save"
                  >
                    Save identity
                  </Button>
                </div>
              )}
              <div className="flex flex-wrap gap-1.5">
                <Button
                  size="sm"
                  className="min-h-[44px] flex-1"
                  disabled={busy !== null || action.disabled}
                  title={action.hint ?? undefined}
                  onClick={() => void runPrimary()}
                  data-testid="git-primary-action"
                >
                  {busyLabel ?? action.label}
                </Button>
                {hasUpstream && behind > 0 && action.kind !== 'pull' && (
                  <Button size="sm" variant="outline" className="min-h-[44px]" disabled={busy !== null} onClick={() => void doPull()} data-testid="git-pull">
                    {busy === 'pull' ? 'Pulling…' : `Pull (↓${behind})`}
                  </Button>
                )}
              </div>
              {action.hint && busy === null && (
                <p className="text-[11px] text-muted-foreground" data-testid="git-primary-hint">{action.hint}</p>
              )}
            </div>

            <div className="flex min-w-0 flex-col gap-1" data-testid="diff-notepad">
              <div className="flex min-w-0 items-center gap-1.5">
                <span className="truncate text-xs font-medium text-muted-foreground">Ask-AI notes</span>
                {notes.trim() && <Badge variant="secondary">{notes.split('\n').length} lines</Badge>}
                <span className="flex-1" />
                <span className="shrink-0 text-[10px] text-muted-foreground">copy only</span>
              </div>
              <Textarea
                className="min-h-40 flex-1 resize-y font-mono text-xs"
                placeholder="Quote a hunk, add your comment, then copy into your harness."
                value={notes}
                onChange={(e) => patchNotepad({ text: e.target.value, copied: false })}
                data-testid="diff-notes"
              />
              <div className="flex gap-1.5">
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
                  onClick={() => patchNotepad({ text: '', copied: false })}
                  data-testid="diff-clear-notes"
                >
                  Clear
                </Button>
              </div>
            </div>
          </div>
        )}
        {/* Ship status: the commit sha, push state, and the AI-drafted PR
            description (copy-only — push already shipped the branch). Kept
            separate from the actions above so success stays visible after
            the poll refreshes the tree clean. */}
        {!status?.notRepo && (ship.sha !== null || ship.state !== 'idle' || ship.pr !== null) && (
          <div className="flex flex-col gap-1.5 rounded-lg border p-2" data-testid="git-ship">
            {ship.sha && (
              <div className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="git-commit-sha">
                <Badge variant="secondary" className="font-mono">{ship.sha}</Badge> committed
              </div>
            )}
            {ship.state !== 'idle' && (
              <p className="text-xs text-muted-foreground" data-testid="git-push-state">
                {ship.state === 'pushing' ? 'Pushing…' : ship.state === 'pushed' ? 'Pushed ✓' : 'Push rejected ✗ — see error above'}
              </p>
            )}
            {ship.sha && !ship.pr && (
              <div className="flex items-center gap-2">
                <span className="flex-1" />
                <Button size="sm" variant="ghost" className="min-h-[36px]" disabled={busy !== null} onClick={() => void draftDescription()} data-testid="git-draft-desc">
                  {busy === 'gen-pr' ? '✨ Drafting…' : '✨ Draft PR description'}
                </Button>
              </div>
            )}
            {ship.pr && (
              <div className="flex flex-col gap-1.5 rounded-md border bg-muted/20 p-2" data-testid="git-pr-desc">
                <p className="text-xs font-medium break-all">{ship.pr.title}</p>
                <p className="text-xs whitespace-pre-wrap break-all text-muted-foreground">{ship.pr.body}</p>
                <Button
                  size="sm"
                  variant="outline"
                  className="min-h-[36px] self-start"
                  onClick={() => void copyToClipboard(`${ship.pr!.title}\n\n${ship.pr!.body}`)}
                  data-testid="git-pr-desc-copy"
                >
                  Copy description
                </Button>
              </div>
            )}
          </div>
        )}

        <GitOpsPrompt projectId={projectId} branch={status?.branch ?? null} />

        {branch.open && (
          <BranchSheet
            branches={branch.list}
            busy={busy}
            newBranch={branch.name}
            onNewBranch={(name) => setBranch((prev) => ({ ...prev, name }))}
            hasUpstream={hasUpstream}
            ahead={ahead}
            onClose={() => setBranch((prev) => ({ ...prev, open: false }))}
            onSwitch={(name, create) => void switchBranch(name, create)}
          />
        )}

        <QuoteDialog
          open={quoteTarget !== null}
          path={quoteTarget?.path ?? ''}
          hunk={quoteTarget?.hunk ?? ''}
          onOpenChange={(open) => {
            if (!open) setQuoteTarget(null)
          }}
          onAdd={addQuote}
        />
      </CardContent>
    </Card>
  )
}
