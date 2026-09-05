import { useCallback, useEffect, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Separator } from '@/components/ui/separator'
import { api, errMsg } from '@/lib/api'

export function PreviewTab({ projectId, onOpenPreview }: { projectId: string; onOpenPreview: () => void }) {
  const [ports, setPorts] = useState<{ port: number; status: string }[]>([])
  const [loadingPort, setLoadingPort] = useState<number | null>(null)
  const [readyPort, setReadyPort] = useState<number | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(async () => {
    try {
      const d = await api<{ ports: { port: number; status: string }[] }>(`/api/projects/${projectId}/preview/ports`)
      setPorts(d.ports ?? [])
    } catch {
      // Keep the last-known list: a transient probe failure shouldn't
      // blink the ports (and their start buttons) away mid-read.
    }
  }, [projectId])

  useEffect(() => {
    refresh()
    const id = setInterval(refresh, 5000)
    return () => clearInterval(id)
  }, [refresh])

  const start = useCallback(async (port: number): Promise<boolean> => {
    setLoadingPort(port)
    setReadyPort(null)
    setError(null)
    try {
      // POST blocks until the sidecar proves the page (or fails loudly) —
      // no client-side guessing, no force-ready.
      await api(`/api/projects/${projectId}/preview/start`, { method: 'POST', body: JSON.stringify({ port }) })
      setReadyPort(port)
      return true
    } catch (e) {
      setError(errMsg(e))
      return false
    } finally {
      setLoadingPort(null)
    }
  }, [projectId])

  // Early launch: as soon as a port appears, start its sidecar in background.
  // Lowest port first: ss order is arbitrary and often leads with ephemeral
  // junk (vite HMR, inspectors), while real servers live down low.
  // The latch resets on failure so a transient start error retries when
  // ports/ready state changes instead of requiring a manual click.
  const hasAutoStarted = useRef(false)
  useEffect(() => {
    if (!hasAutoStarted.current && ports.length > 0 && readyPort === null && loadingPort === null) {
      hasAutoStarted.current = true
      const first = [...ports].sort((a, b) => a.port - b.port)[0]
      void start(first.port).then((ok) => {
        if (!ok) hasAutoStarted.current = false
      })
    }
  }, [ports, readyPort, loadingPort, start])

  async function closePreview() {
    try {
      await api(`/api/projects/${projectId}/preview`, { method: 'DELETE' })
      setReadyPort(null)
    } catch (e) {
      setError(errMsg(e))
    }
  }

  return (
    <Card className="flex h-full w-full flex-col shadow-sm" data-testid="preview-tab">
      <CardHeader className="pb-3">
        <div className="flex items-center gap-2">
          <div className="flex items-center gap-2">
            <div className="size-2 rounded-full bg-emerald-500 animate-pulse" />
            <CardTitle className="text-base">Preview</CardTitle>
          </div>
          <Badge variant="outline" className="ml-2 font-normal">
            {ports.length} {ports.length === 1 ? 'port' : 'ports'} live
          </Badge>
          <span className="flex-1" />
          <Button size="sm" variant="outline" onClick={refresh} data-testid="preview-refresh">
            ↻ Refresh
          </Button>
        </div>
        <CardDescription>
          Listening ports in your project — including servers bound to all interfaces (<span className="font-mono">vite --host 0.0.0.0</span> shows up here). The previewer's own ports stay hidden. Pick a port, then Open it: Chromium runs beside your project, so <span className="font-mono">localhost</span> just works.
        </CardDescription>
      </CardHeader>
      <Separator />
      <CardContent className="flex flex-1 flex-col gap-4 pt-4">
        {ports.length === 0 ? (
          <div className="grid place-items-center rounded-lg border border-dashed bg-muted/30 p-8 text-center" data-testid="preview-empty">
            <div className="flex flex-col items-center gap-2">
              <span className="text-2xl">○</span>
              <p className="text-sm font-medium">No active localhost ports</p>
              <p className="text-xs text-muted-foreground">Start a server with a quick command, then hit Refresh.</p>
            </div>
          </div>
        ) : (
          <div className="flex flex-wrap gap-2" data-testid="preview-ports">
            {ports.map((s) => (
              <Button
                key={s.port}
                size="sm"
                variant={readyPort === s.port ? 'secondary' : 'outline'}
                className="gap-1.5"
                onClick={() => start(s.port)}
                disabled={loadingPort !== null}
                data-testid={`preview-port-${s.port}`}
              >
                <span className={`size-1.5 rounded-full ${readyPort === s.port ? 'bg-emerald-500' : loadingPort === s.port ? 'bg-amber-500 animate-pulse' : 'bg-sky-500'}`} />
                {loadingPort === s.port ? 'Starting…' : `:${s.port}`}
              </Button>
            ))}
          </div>
        )}
        {loadingPort !== null && (
          <div className="flex items-center gap-2 text-xs text-muted-foreground" data-testid="preview-loading">
            <span className="size-3 animate-spin rounded-full border-2 border-muted-foreground/30 border-t-foreground" />
            Getting preview ready for :{loadingPort}…
          </div>
        )}
        {readyPort !== null && (
          <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 p-3">
            <span className="text-xs text-muted-foreground">Ready</span>
            <Badge variant="secondary">:{readyPort}</Badge>
            <span className="flex-1" />
            <Button size="sm" onClick={onOpenPreview} data-testid={`preview-open-${readyPort}`}>
              Open :{readyPort} ↗
            </Button>
            <Button size="sm" variant="outline" onClick={closePreview} data-testid={`preview-close-${readyPort}`}>
              Close
            </Button>
          </div>
        )}
        {error && <p className="text-xs text-destructive" data-testid="preview-error">{error}</p>}
        <p className="mt-auto text-xs text-muted-foreground">
          Tip: each port opens in the same Chromium — like browser tabs. Close when done to free resources.
        </p>
      </CardContent>
    </Card>
  )
}
