import { RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'

import { TerminalView } from '@/components/terminal/TerminalView'
import { LoginForm } from '@/components/LoginForm'
import { LandingPage } from '@/components/LandingPage'
import { Home } from '@/components/Home'
import { PreviewSurface } from '@/components/PreviewSurface'
import { usePullToRefresh } from '@/hooks/usePullToRefresh'
import { parsePreviewPath, parseTerminalPath } from '@/lib/paths'

type AuthState = 'loading' | 'out' | 'in'

// Routes: / is the landing page, /login is login, /app is the app.
// Terminal (/projects/{id}/terminal/{session}) and preview (/preview/{id})
// keep their deep-link URLs. usePath is the whole router: pushState for
// navigation, popstate for back, replaceState for redirects.
function usePath(): [string, (to: string) => void, (to: string) => void] {
  const [path, setPath] = useState(window.location.pathname)
  useEffect(() => {
    const onPop = () => setPath(window.location.pathname)
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])
  const navigate = useCallback((to: string) => {
    window.history.pushState({}, '', to)
    setPath(to)
  }, [])
  const replace = useCallback((to: string) => {
    window.history.replaceState({}, '', to)
    setPath(to)
  }, [])
  return [path, navigate, replace]
}

export default function App() {
  const [state, setState] = useState<AuthState>('loading')
  const [email, setEmail] = useState('')
  const [path, navigate, replace] = usePath()
  const pulling = usePullToRefresh()

  useEffect(() => {
    fetch('/api/auth/me')
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((data: { email: string }) => {
        setEmail(data.email)
        setState('in')
      })
      .catch(() => setState('out'))
  }, [])

  // Bounce logged-in users off /login to the canonical /app URL
  // (unknown paths normalize there too). / never bounces: it is the
  // landing page with the getting-started guide, logged in or not.
  useEffect(() => {
    if (state !== 'in') return
    if (path === '/app' || path === '/' || parseTerminalPath(path) !== null || parsePreviewPath(path) !== null) return
    replace('/app')
  }, [state, path, replace])

  if (state === 'loading') {
    return (
      <main className="grid min-h-dvh place-items-center">
        <p className="text-muted-foreground text-sm">Loading…</p>
      </main>
    )
  }

  const onLoggedIn = (e: string) => {
    setEmail(e)
    setState('in')
    navigate('/app')
  }
  const onLogout = () => {
    setState('out')
    navigate('/')
  }
  const login = <LoginForm onLoggedIn={onLoggedIn} navigate={navigate} />
  const home = <Home email={email} onLogout={onLogout} navigate={navigate} />

  // Logged out: landing at /, login at /login. Deep links (/app,
  // /projects/..., /preview/...) show login; after login the same path
  // renders, so a shared terminal link lands where it points.
  if (state === 'out') {
    if (path === '/') {
      return (
        <>
          {pulling && <PullToRefreshIndicator />}
          <LandingPage onLogin={() => navigate('/login')} />
        </>
      )
    }
    return (
      <>
        {pulling && <PullToRefreshIndicator />}
        {login}
      </>
    )
  }

  // Logged in: / is still the landing page (with an Open app button);
  // /app is home; terminal and preview keep their URLs.
  if (path === '/') {
    return (
      <>
        {pulling && <PullToRefreshIndicator />}
        <LandingPage onLogin={() => navigate('/app')} loggedIn />
      </>
    )
  }
  const terminal = parseTerminalPath(path)
  if (terminal) {
    return (
      <>
        {pulling && <PullToRefreshIndicator />}
        <TerminalView
          projectId={terminal.projectId}
          initialSession={terminal.session}
          onBack={() => navigate('/app')}
          onOpenPreview={() => window.open(`/preview/${encodeURIComponent(terminal.projectId)}`, '_blank')}
        />
      </>
    )
  }
  const previewId = parsePreviewPath(path)
  if (previewId !== null) {
    return (
      <>
        {pulling && <PullToRefreshIndicator />}
        <PreviewSurface projectId={previewId} />
      </>
    )
  }
  return (
    <>
      {pulling && <PullToRefreshIndicator />}
      {home}
    </>
  )
}

function PullToRefreshIndicator() {
  return (
    <div className="fixed top-0 left-0 right-0 z-[50] flex items-center justify-center gap-2 bg-background/95 py-2 backdrop-blur-sm">
      <RefreshCw className="size-4 animate-spin text-primary" />
      <span className="text-xs text-muted-foreground">Release to refresh</span>
    </div>
  )
}
