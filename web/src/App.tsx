import { useCallback, useEffect, useState } from 'react'

import { TerminalView } from '@/components/terminal/TerminalView'
import { LoginForm } from '@/components/LoginForm'
import { LandingPage } from '@/components/LandingPage'
import { Home } from '@/components/Home'
import { PreviewSurface } from '@/components/PreviewSurface'
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

  useEffect(() => {
    fetch('/api/auth/me')
      .then((res) => (res.ok ? res.json() : Promise.reject(new Error(`HTTP ${res.status}`))))
      .then((data: { email: string }) => {
        setEmail(data.email)
        setState('in')
      })
      .catch(() => setState('out'))
  }, [])

  // Bounce logged-in users off / and /login to the canonical /app URL
  // (unknown paths normalize there too). An effect, so render stays pure
  // and the router state never disagrees with the address bar.
  useEffect(() => {
    if (state !== 'in') return
    if (path === '/app' || parseTerminalPath(path) !== null || parsePreviewPath(path) !== null) return
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
      return <LandingPage onLogin={() => navigate('/login')} />
    }
    return login
  }

  // Logged in: /app is home; terminal and preview keep their URLs.
  const terminal = parseTerminalPath(path)
  if (terminal) {
    return (
      <TerminalView
        projectId={terminal.projectId}
        initialSession={terminal.session}
        onBack={() => navigate('/app')}
        onOpenPreview={() => window.open(`/preview/${encodeURIComponent(terminal.projectId)}`, '_blank')}
      />
    )
  }
  const previewId = parsePreviewPath(path)
  if (previewId !== null) {
    return <PreviewSurface projectId={previewId} />
  }
  return home
}
