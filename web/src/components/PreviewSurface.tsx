import { useEffect, useRef, useState } from 'react'

import { api } from '@/lib/api'
import { previewSurfacePath } from '@/lib/preview'

// The project is not loaded in this iframe. It loads the authenticated noVNC
// surface, which keeps the browser chrome and project traffic server-side.
//
// The noVNC view uses resize=scale (see lib/preview) so the framebuffer
// fills the iframe during the async backend resize, then converges to ~1:1
// once this page fits the sidecar Chromium window to the iframe: after load
// and on every (debounced) resize it reports the iframe size, and the
// backend resizes the Chromium window to match.
// The previewed app then reflows like a real browser window instead of
// cropping a fixed-size desktop. Syncing starts on iframe load — never
// before — so merely opening the page can't resurrect a closed sidecar.
export function PreviewSurface({ projectId, onBack }: { projectId: string; onBack: () => void }) {
  const frameRef = useRef<HTMLIFrameElement>(null)
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    const frame = frameRef.current
    if (!frame || !loaded || typeof ResizeObserver === 'undefined') return
    let timer: ReturnType<typeof setTimeout> | null = null
    const sync = () => {
      const width = Math.round(frame.clientWidth)
      const height = Math.round(frame.clientHeight)
      if (width < 100 || height < 100) return
      api(`/api/projects/${projectId}/preview/tools/viewport`, {
        method: 'POST',
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
  }, [projectId, loaded])

  return (
    <main className="flex min-h-dvh flex-col">
      <header className="flex items-center gap-3 border-b p-3">
        <button type="button" className="text-sm text-muted-foreground" onClick={onBack}>Back</button>
        <h1 className="text-sm font-semibold">Preview</h1>
      </header>
      <iframe
        ref={frameRef}
        title="Remote project preview"
        className="min-h-0 flex-1 border-0"
        src={previewSurfacePath(projectId)}
        allow="clipboard-read; clipboard-write"
        onLoad={() => setLoaded(true)}
      />
    </main>
  )
}
