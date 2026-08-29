import { useCallback, useEffect, useRef, useState } from 'react'

import { terminalPath } from '@/lib/paths'
import { api, errMsg } from '@/lib/api'
import type { Harness } from '@/lib/types'
import { TerminalHeader } from '@/components/terminal/TerminalHeader'
import { TerminalPane, type ConnStatus } from '@/components/terminal/TerminalPane'
import { NewSessionDialog } from '@/components/terminal/NewSessionDialog'

// The terminal screen: header (status, session picker, actions) above the
// live terminal pane. Owns which session is attached and the shared status/
// error state; the pane owns xterm and the websocket.
export function TerminalView({ projectId, initialSession, onBack }: {
  projectId: string
  initialSession: string
  onBack: () => void
}) {
  const [current, setCurrent] = useState(initialSession)
  const [status, setStatus] = useState<ConnStatus>('connecting')
  const [error, setError] = useState<string | null>(null)
  const [sessions, setSessions] = useState<{ name: string }[]>([])
  const [harnesses, setHarnesses] = useState<Harness[]>([])
  const [redial, setRedial] = useState(0)
  const [newDialogOpen, setNewDialogOpen] = useState(false)
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
    <div className="flex h-dvh flex-col gap-3 bg-muted/40 p-3">
      <TerminalHeader
        projectId={projectId}
        current={current}
        sessions={sessions}
        status={status}
        onBack={onBack}
        onSwitch={switchSession}
        onNewSession={() => setNewDialogOpen(true)}
        onRestart={restart}
        onRename={rename}
        onKill={kill}
      />

      {error && (
        <p className="max-h-24 overflow-auto rounded-lg border border-destructive/30 bg-destructive/10 px-3 py-2 text-xs break-all text-destructive">
          {error}
        </p>
      )}

      <div className="min-h-0 flex-1 overflow-hidden rounded-xl border bg-black p-2 shadow-sm">
        <div ref={hostRef} className="h-full" />
        <TerminalPane
          projectId={projectId}
          session={current}
          redial={redial}
          hostRef={hostRef}
          onStatus={setStatus}
          onError={setError}
        />
      </div>

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
