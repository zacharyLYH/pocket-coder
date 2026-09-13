import { useEffect, useState } from 'react'
import { Check, ChevronLeft, Copy, FileWarning } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { api, errMsg, projectPath } from '@/lib/api'
import type { CodemapFile } from '@/lib/types'

// FileOverlay is the readonly snippet viewer: a full screen layer over the
// mounted CodemapTab (no route change, so scroll and prompt survive).
// No inputs, no edits, nothing to persist.
export function FileOverlay({ projectId, path, start, end, sha, onBack }: {
  projectId: string
  path: string
  start: number
  end: number
  sha: string
  onBack: () => void
}) {
  const [file, setFile] = useState<CodemapFile | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)

  useEffect(() => {
    let cancelled = false
    api<CodemapFile>(
      projectPath(projectId, `/file?path=${encodeURIComponent(path)}&start=${start}&end=${end}&sha=${encodeURIComponent(sha)}`),
    )
      .then((d) => { if (!cancelled) setFile(d) })
      .catch((e) => { if (!cancelled) setError(errMsg(e)) })
    return () => { cancelled = true }
  }, [projectId, path, start, end, sha])

  const lines = (file?.content ?? '').split('\n')

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-background" data-testid="file-overlay" data-path={path}>
      <header className="flex shrink-0 items-center gap-1.5 border-b px-2 py-2">
        <Button size="sm" variant="ghost" onClick={onBack} data-testid="file-back" className="min-h-[40px]">
          <ChevronLeft className="size-4" />
          Back
        </Button>
        <span className="min-w-0 flex-1 truncate font-mono text-xs font-medium">{path}</span>
        <Badge variant="secondary" className="shrink-0 font-mono font-normal">L{start}-{end}</Badge>
        {file && file.content !== '' && !file.binary && (
          <Button
            size="icon"
            variant="ghost"
            aria-label="Copy file excerpt"
            onClick={() => {
              void navigator.clipboard.writeText(file.content).catch(() => {})
              setCopied(true)
              setTimeout(() => setCopied(false), 1500)
            }}
          >
            {copied ? <Check className="size-4" /> : <Copy className="size-4" />}
          </Button>
        )}
      </header>
      {file?.moved && (
        <p className="flex shrink-0 items-center gap-1.5 border-b bg-amber-500/10 px-3 py-1.5 text-xs text-muted-foreground" data-testid="file-moved">
          <FileWarning className="size-3.5 shrink-0" />
          Tree moved since this codemap was generated — lines may have shifted.
        </p>
      )}
      <div className="min-h-0 flex-1 overflow-auto p-3">
        {error ? (
          <p className="text-xs text-destructive">{error}</p>
        ) : file === null ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-3.5 w-full" />
            <Skeleton className="h-3.5 w-11/12" />
            <Skeleton className="h-3.5 w-4/5" />
          </div>
        ) : file.binary ? (
          <p className="text-xs text-muted-foreground">Binary file, not shown. Open it in the terminal.</p>
        ) : file.content === '' ? (
          <p className="text-xs text-muted-foreground">File moved or range is empty since generation.</p>
        ) : (
          <pre className="overflow-x-auto rounded-xl border bg-muted/40 p-3 font-mono text-xs leading-5" data-testid="file-content">
            {lines.map((line, i) => (
              <div key={i} className="flex">
                <span className="w-8 shrink-0 pr-3 text-right text-muted-foreground select-none">{start + i}</span>
                <span className="flex-1 rounded bg-amber-500/20 px-1">{line || ' '}</span>
              </div>
            ))}
          </pre>
        )}
      </div>
    </div>
  )
}
