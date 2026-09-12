import { useCallback, useEffect, useRef, useState } from 'react'

import { terminalPath } from '@/lib/paths'
import { api, errMsg } from '@/lib/api'
import type { Harness } from '@/lib/types'
import { Button } from '@/components/ui/button'
import { TerminalHeader } from '@/components/terminal/TerminalHeader'
import { TerminalPane, type ConnStatus } from '@/components/terminal/TerminalPane'
import { NewSessionDialog } from '@/components/terminal/NewSessionDialog'
import { PreviewTab } from '@/components/terminal/PreviewTab'
import { LogsTab } from '@/components/terminal/LogsTab'

// The terminal screen: header (status, session picker, actions) above the
// live terminal pane. Owns which session is attached and the shared status/
// error state; the pane owns xterm and the websocket.
export function TerminalView({ projectId, initialSession, onBack, onOpenPreview }: {
  projectId: string
  initialSession: string
  onBack: () => void
  onOpenPreview: () => void
}) {
  const [current, setCurrent] = useState(initialSession)
  const [status, setStatus] = useState<ConnStatus>('connecting')
  const [error, setError] = useState<string | null>(null)
  const [sessions, setSessions] = useState<{ name: string }[]>([])
  const [harnesses, setHarnesses] = useState<Harness[]>([])
  const [redial, setRedial] = useState(0)
  const [newDialogOpen, setNewDialogOpen] = useState(false)
  const [tab, setTab] = useState<'terminal' | 'preview' | 'logs'>('terminal')
  const hostRef = useRef<HTMLDivElement>(null)

  // ─── data fetching ──────────────────────────────────────────────────
  const refreshSessions = useCallback(() => {
    api<{ sessions: { name: string }[] }>(`/api/projects/${projectId}/sessions`)
      .then((d) => setSessions(d.sessions))
      .catch(() => {})
  }, [projectId])

  useEffect(() => { refreshSessions() }, [refreshSessions, redial])

  useEffect(() => {
    api<{ harnesses: Harness[] }>(`/api/projects/${projectId}/harnesses`)
      .then((d) => setHarnesses(d.harnesses))
      .catch(() => {})
  }, [projectId])

  // Keep the URL in step with the attached session (replaceState: back
  // returns to the project list, not through every session).
  useEffect(() => {
    window.history.replaceState({}, '', terminalPath(projectId, current))
  }, [projectId, current])

  // ─── actions ────────────────────────────────────────────────────────
  function switchSession(name: string) {
    // Allow re-entering the same session after kill (status === 'ended')
    // so the ensure path can relaunch a killed harness.
    if (name === current && status !== 'ended') return
    setCurrent(name)
    setError(null)
    // redial triggers the pane effect
    setRedial((n) => n + 1)
  }

  async function restart() {
    try {
      await api(`/api/projects/${projectId}/sessions/${current}/restart`, { method: 'POST' })
      setRedial((n) => n + 1)
    } catch (err) {
      setError(errMsg(err))
    }
  }

  async function kill() {
    try {
      await api(`/api/projects/${projectId}/sessions/${current}`, { method: 'DELETE' })
      setStatus('ended')
      refreshSessions()
    } catch (err) {
      setError(errMsg(err))
    }
  }

  // Delete removes the session outright (tmux kill + state.json metadata),
  // so it disappears from the picker for good. Deleting the attached session
  // lands the user on the next remaining session instead of a dead screen;
  // the API refuses to delete the last one, so a fallback is never needed.
  const del = useCallback(async (name: string) => {
    try {
      await api(`/api/projects/${projectId}/sessions/${name}/delete`, { method: 'DELETE' })
      if (name === current) {
        const d = await api<{ sessions: { name: string }[] }>(`/api/projects/${projectId}/sessions`)
        const next = d.sessions.map((s) => s.name).find((n) => n !== name)
        if (!next) throw new Error('cannot delete the last session')
        setSessions(d.sessions)
        setCurrent(next)
        setError(null)
        setRedial((n) => n + 1)
      } else {
        refreshSessions()
      }
    } catch (err) {
      throw new Error(errMsg(err))
    }
  }, [projectId, current, refreshSessions])

  async function rename(newName: string) {
    await api(`/api/projects/${projectId}/sessions/${current}/rename`, {
      method: 'POST',
      body: JSON.stringify({ name: newName }),
    })
    const old = current
    setCurrent(newName)
    setError(null)
    // keep session list in sync; the renamed session stays attached so no redial needed
    setSessions((prev) => prev.map((s) => (s.name === old ? { name: newName } : s)))
    refreshSessions()
  }

  function onLaunched(name: string) {
    refreshSessions()
    setCurrent(name)
    setError(null)
    setRedial((n) => n + 1)
  }

  return (
    <div className="flex h-dvh w-full flex-col gap-3 bg-muted/40 p-0">
      <div className="px-3 pt-3">
        <TerminalHeader
          projectId={projectId}
          current={current}
          sessions={sessions}
          status={status}
          onBack={onBack}
          onSwitch={switchSession}
          onDelete={del}
          onNewSession={() => setNewDialogOpen(true)}
          onRestart={restart}
          onRename={rename}
          onKill={kill}
        />
      </div>
      <div className="flex gap-2 px-3">
        <Button className="flex-1" size="sm" variant={tab === 'terminal' ? 'secondary' : 'outline'} onClick={() => setTab('terminal')} data-testid="tab-terminal">Terminal</Button>
        <Button className="flex-1" size="sm" variant={tab === 'preview' ? 'secondary' : 'outline'} onClick={() => setTab('preview')} data-testid="tab-preview">Preview</Button>
        <Button className="flex-1" size="sm" variant={tab === 'logs' ? 'secondary' : 'outline'} onClick={() => setTab('logs')} data-testid="tab-logs">Logs</Button>
      </div>

      {error && (
        <p className="max-h-24 overflow-auto rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">
          {error}
        </p>
      )}

      {tab === 'preview' ? (
        <div className="min-h-0 flex-1 w-full px-3 pb-3">
          <PreviewTab projectId={projectId} onOpenPreview={onOpenPreview} />
        </div>
      ) : tab === 'logs' ? (
        <div className="min-h-0 flex-1 w-full px-3 pb-3">
          <LogsTab projectId={projectId} />
        </div>
      ) : (
        <div className="min-h-0 flex-1 overflow-hidden border bg-black p-2 shadow-sm mx-3 mb-3 rounded-xl">
          <div ref={hostRef} className="h-full w-full" />
          <TerminalPane
            projectId={projectId}
            session={current}
            redial={redial}
            hostRef={hostRef}
            onStatus={setStatus}
            onError={setError}
          />
        </div>
      )}

      <NewSessionDialog
        open={newDialogOpen}
        onOpenChange={(open) => {
          setNewDialogOpen(open)
          if (open) setError(null)
        }}
        projectId={projectId}
        harnesses={harnesses}
        onLaunched={onLaunched}
      />
    </div>
  )
}
