import { useEffect, useRef, useState } from 'react'

// usePullToRefresh: JS-based pull-to-refresh for the PWA home view.
// iOS standalone hides the native refresh gesture behind sticky headers
// and short content, so we detect a downward drag at the top edge and
// reload the session. Shows a spinner while "refreshing".
//
// A drag that starts on an interactive overlay (the draggable Butler FAB,
// its sheet, dialogs, …) must never trigger a refresh — the FAB uses
// touch-action:none + pointer capture, but the browser still fires the
// companion touch events which bubble to document. So any touch whose
// target lives inside [data-no-pull-refresh] is ignored.
const OPT_OUT_ATTR = 'data-no-pull-refresh'

function pullTarget(e: TouchEvent): EventTarget | null {
  const t = e.touches[0] ?? e.changedTouches[0]
  return (t?.target as EventTarget | undefined) ?? e.target
}

export function isPullRefreshOptOut(target: EventTarget | null): boolean {
  if (!(target instanceof Element)) return false
  return target.closest(`[${OPT_OUT_ATTR}]`) !== null
}

export function usePullToRefresh(onRefresh?: () => void) {
  const [active, setActive] = useState(false)
  const startY = useRef(0)
  const delta = useRef(0)
  // Deliberately stiff: a refresh is a full reload, so the drag must
  // travel well past a casual scroll.
  const threshold = 110

  useEffect(() => {
    if (!('ontouchstart' in window)) return
    if (onRefresh) return // controlled mode handled by caller
    function onTouchStart(e: TouchEvent) {
      if (isPullRefreshOptOut(pullTarget(e))) { startY.current = 0; return }
      if (window.scrollY === 0) startY.current = e.touches[0].clientY
      else startY.current = 0
    }
    function onTouchMove(e: TouchEvent) {
      if (!startY.current) return
      if (isPullRefreshOptOut(pullTarget(e))) { startY.current = 0; delta.current = 0; setActive(false); return }
      const d = e.touches[0].clientY - startY.current
      if (d > 0 && window.scrollY === 0) {
        e.preventDefault()
        delta.current = d
        if (d > 20) setActive(true)
      }
    }
    function onTouchEnd(e: TouchEvent) {
      if (!isPullRefreshOptOut(e.target)) {
        if (delta.current > threshold) window.location.reload()
      }
      startY.current = 0
      delta.current = 0
      setActive(false)
    }
    document.addEventListener('touchstart', onTouchStart, { passive: true })
    document.addEventListener('touchmove', onTouchMove, { passive: false })
    document.addEventListener('touchend', onTouchEnd, { passive: true })
    return () => {
      document.removeEventListener('touchstart', onTouchStart)
      document.removeEventListener('touchmove', onTouchMove)
      document.removeEventListener('touchend', onTouchEnd)
    }
  }, [onRefresh])

  return active
}
