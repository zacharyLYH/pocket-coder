import { useEffect, useState } from 'react'

import { TerminalView } from '@/components/terminal/TerminalView'
import { LoginForm } from '@/components/LoginForm'
import { Home } from '@/components/Home'
import { PreviewSurface } from '@/components/PreviewSurface'
import { parsePreviewPath, parseTerminalPath } from '@/lib/paths'

type AuthState = 'loading' | 'out' | 'in'

// usePath is the whole router: pushState for navigation, popstate for back.
// Anything that is not a terminal path renders the home page.
function usePath(): [string, (to: string) => void] {
  const [path, setPath] = useState(window.location.pathname)
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname)
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])
  const navigate = (to: string) => {
    window.history.pushState({}, '', to)
    setPath(to)
  }
  return [path, navigate]
}

export default function App() {
  const [state, setState] = useState<AuthState>('loading')
  const [email, setEmail] = useState('')
  const [path, navigate] = usePath()

  useEffect(() => {
    fetch('/api/auth/me')
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((data: { email: string }) => {
        setEmail(data.email)
        setState('in')
      })
      .catch(() => setState('out'))
  }, [])

  if (state === 'loading') {
    return (
      <main className="grid min-h-dvh place-items-center">
        <p className="text-muted-foreground text-sm">Loading…</p>
      </main>
    )
  }
  if (state === 'out') {
    return <LoginForm onLoggedIn={(e) => { setEmail(e); setState('in') }} />
  }
  const terminal = parseTerminalPath(path)
  if (terminal) {
    return (
      <TerminalView
        projectId={terminal.projectId}
        initialSession={terminal.session}
        onBack={() => navigate('/')}
        onOpenPreview={() => window.open(`/preview/${encodeURIComponent(terminal.projectId)}`, '_blank')}
      />
    )
  }
  const previewId = parsePreviewPath(path)
  if (previewId !== null) {
    return <PreviewSurface projectId={previewId} onBack={() => navigate('/')} />
  }
  return <Home email={email} onLogout={() => setState('out')} navigate={navigate} />
}
