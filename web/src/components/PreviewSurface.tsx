import { useCallback, useEffect, useRef, useState } from 'react'
import { ArrowLeft } from 'lucide-react'

import { ApiError, api, projectPath } from '@/lib/api'
import { PREVIEW_HEARTBEAT_MS, previewSurfacePath } from '@/lib/preview'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'

function isTouchDevice(): boolean {
  // (pointer: coarse) is the reliable mobile signal in real browsers.
  // maxTouchPoints is a fallback for Playwright's hasTouch simulation and
  // some touchscreen laptops, but we gate it on a small viewport so desktop
  // touchscreens don't spuriously show the back button.
  return (
    (window.matchMedia?.('(pointer: coarse)').matches ?? false) ||
    ((navigator.maxTouchPoints ?? 0) > 0 && window.innerWidth <= 768)
  )
}

// The project is not loaded in this iframe. It loads the authenticated noVNC
// surface, which keeps the browser chrome and project traffic server-side.
//
// The page is chromeless on purpose: it opens in its own tab and the
// sidecar stays warm behind it, so leaving means closing the tab —
// reopening reattaches to the same browser instantly. Stopping the sidecar
// is the Close button's job in the Preview tab.
//
// On mobile PWAs (standalone mode), window.open('_blank') opens in the same
// window, so App.tsx navigates here inline. Users dismiss via native back
// (swipe-from-edge on iOS, hardware back on Android) or the back button.
// The pt-[env(safe-area-inset-top)] keeps noVNC content below the status bar.
//
// On mobile, a back button is rendered because swipe-back gestures are
// captured by the VNC canvas (cursor moves inside the remote desktop).
//
// The noVNC view uses resize=scale (see lib/preview) so the framebuffer
// fills the iframe, then converges to ~1:1 once this page fits the sidecar
// Chromium window to the iframe: after load and on every (debounced) resize
// it reports the iframe size, and the backend resizes the Chromium window
// to match. Syncing starts on iframe load — never before — so merely
// opening the page can't resurrect a closed sidecar.
export function PreviewSurface({ projectId }: { projectId: string }) {
  const frameRef = useRef<HTMLIFrameElement>(null)
  const [loaded, setLoaded] = useState(false)
  const [token, setToken] = useState<string | null>(null)
  const [expired, setExpired] = useState(false)
  const [loading, setLoading] = useState(true)
  const [showBack, setShowBack] = useState(() => isTouchDevice())

  // The gate includes viewport width, so re-evaluate on resize (rotation,
  // window drag): a mount-time check alone would strand the button on or
  // off after the viewport crosses 768px. Same-value sets are a React
  // no-op, so steady-state resizes cost nothing.
  useEffect(() => {
    const onResize = () => setShowBack(isTouchDevice())
    window.addEventListener('resize', onResize)
    return () => window.removeEventListener('resize', onResize)
  }, [])

  const fetchToken = useCallback(() => {
    setExpired(false)
    setLoaded(false)
    setToken(null)
    setLoading(true)
    api<{ token?: string }>(projectPath(projectId, '/preview'))
      .then((s) => setToken(s.token ?? null))
      .catch(() => setToken(null))
      .finally(() => setLoading(false))
  }, [projectId])

  useEffect(() => {
    fetchToken()
  }, [fetchToken])

  useEffect(() => {
    if (!token || expired || typeof window === 'undefined') return
    const beat = () => {
      api(projectPath(projectId, '/preview/heartbeat'), {
        method: 'POST',
        headers: { 'X-Preview-Token': token },
      }).catch((err) => {
        if (err instanceof ApiError && err.status === 404) setExpired(true)
      })
    }
    beat()
    const t = setInterval(beat, PREVIEW_HEARTBEAT_MS)
    const onVisible = () => {
      if (document.visibilityState === 'visible') beat()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      clearInterval(t)
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [projectId, token, expired])

  useEffect(() => {
    const frame = frameRef.current
    if (!frame || !loaded || !token || typeof ResizeObserver === 'undefined') return
    let timer: ReturnType<typeof setTimeout> | null = null
    const sync = () => {
      const width = Math.round(frame.clientWidth)
      const height = Math.round(frame.clientHeight)
      if (width < 100 || height < 100) return
      api(projectPath(projectId, '/preview/tools/viewport'), {
        method: 'POST',
        headers: { 'X-Preview-Token': token },
        body: JSON.stringify({ width, height }),
      }).catch(() => {
        // Preview sidecar not up yet or gone — the next resize (or reopen)
        // syncs again. Never break the surface over a fit request.
      })
    }
    sync()
    const schedule = () => {
      if (timer) clearTimeout(timer)
      timer = setTimeout(sync, 500)
    }
    const ro = new ResizeObserver(schedule)
    ro.observe(frame)
    // A backgrounded tab's timers can be throttled, so a resize that
    // happened while hidden might not have synced yet — catch up on return.
    const onVisible = () => {
      if (document.visibilityState === 'visible') schedule()
    }
    document.addEventListener('visibilitychange', onVisible)
    return () => {
      if (timer) clearTimeout(timer)
      ro.disconnect()
      document.removeEventListener('visibilitychange', onVisible)
    }
  }, [projectId, loaded, token])

  if (expired) {
    return (
      <main className="grid h-dvh w-full place-items-center p-6">
        <Card className="max-w-md text-center">
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Preview expired — the sidecar rotated its token.
            </p>
            <Button className="mt-4" onClick={fetchToken}>
              Resume
            </Button>
          </CardContent>
        </Card>
      </main>
    )
  }

  if (!loading && !token) {
    return (
      <main className="grid h-dvh w-full place-items-center p-6">
        <Card className="max-w-md text-center">
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Preview not running — start it from the Preview tab.
            </p>
          </CardContent>
        </Card>
      </main>
    )
  }

  return (
    <main
      className="relative h-dvh w-full"
      data-no-pull-refresh
      style={{
        paddingTop: 'env(safe-area-inset-top)',
      }}
    >
      {showBack && (
        <Button
          variant="ghost"
          size="sm"
          className="absolute rounded-full p-2 shadow-md backdrop-blur-sm"
          style={{
            top: 'max(0.5rem, env(safe-area-inset-top))',
            left: '0.5rem',
          }}
          onClick={() => {
            if (window.history.length > 1) {
              window.history.back()
            } else {
              window.location.href = '/app'
            }
          }}
          aria-label="Back to terminal"
        >
          <ArrowLeft className="h-4 w-4" />
        </Button>
      )}
      {token && (
        <iframe
          ref={frameRef}
          title="Remote project preview"
          className="h-full w-full border-0"
          src={previewSurfacePath(projectId, token)}
          allow="clipboard-read; clipboard-write"
          onLoad={() => setLoaded(true)}
        />
      )}
    </main>
  )
}
