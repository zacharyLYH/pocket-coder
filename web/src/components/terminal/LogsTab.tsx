import { useEffect, useRef } from 'react'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { useProjectLogs } from '@/hooks/useProjectLogs'

function timeOfDay(iso: string): string {
  const t = new Date(iso)
  return Number.isNaN(t.getTime()) ? '' : t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })
}

// Logs tab: a tail -f over the project's running log. Oldest at top;
// follows new lines only while pinned to the bottom, so reading history
// never yanks the scroll out from under you.
export function LogsTab({ projectId }: { projectId: string }) {
  const { logs } = useProjectLogs(projectId)
  const bodyRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = bodyRef.current
    if (!el) return
    const pinned = el.scrollHeight - el.scrollTop - el.clientHeight < 48
    if (pinned) el.scrollTop = el.scrollHeight
  }, [logs])

  return (
    <Card className="flex h-full w-full flex-col shadow-sm" data-testid="logs-tab">
      <CardHeader className="pb-3">
        <CardTitle className="text-base">Logs</CardTitle>
        <CardDescription>Running log for this project — preview and session activity lands here.</CardDescription>
      </CardHeader>
      <CardContent className="min-h-0 flex-1">
        {logs.length === 0 ? (
          <p className="text-xs text-muted-foreground" data-testid="logs-empty">
            No logs yet — open a preview or run a session.
          </p>
        ) : (
          <div ref={bodyRef} className="h-full overflow-auto rounded-lg bg-black p-3 font-mono text-xs text-zinc-200" data-testid="logs-list">
            {logs.map((l) => (
              <div key={l.id} className="flex gap-2 py-px">
                <span className="shrink-0 text-zinc-500">{timeOfDay(l.time)}</span>
                <span className="shrink-0 text-sky-400">{l.type}</span>
                <span className="break-all">{l.message}</span>
              </div>
            ))}
          </div>
        )}
      </CardContent>
    </Card>
  )
}
