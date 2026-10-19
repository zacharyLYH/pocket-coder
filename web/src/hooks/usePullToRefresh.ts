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

// Terminal page gate: refresh only starts above the sessions tabs. The tab
// strip sits below the header — drags starting on/below it belong to the
// terminal pane, fixed views, or tab scrolling, never to refresh. Returns
// null when not on the terminal page (no gating).
function terminalPullGate(): number | null {
  if (typeof document === 'undefined') return null
  if (!document.querySelector('[data-testid="terminal-root"]')) return null
  const tabs = document.querySelector('[data-testid="terminal-tabs"]')
  if (!(tabs instanceof HTMLElement)) return null
  return tabs.getBoundingClientRect().top
}

export function isTerminalPullAllowed(startY: number): boolean {
  const gate = terminalPullGate()
  if (gate === null) return true
  return startY < gate
}

export function usePullToRefresh(onRefresh?: () => void) {
  const [active, setActive] = useState(false)
  const startY = useRef(0)
  const delta = useRef(0)
  // Deliberately stiff: a refresh is a full reload, so the drag must
  // travel at least 1/3 of the viewport height — casual scrolls and
  // short flicks never trigger it.
  function threshold(): number {
    return Math.max(110, window.innerHeight / 3)
  }

  useEffect(() => {
    if (!('ontouchstart' in window)) return
    if (onRefresh) return // controlled mode handled by caller
    function onTouchStart(e: TouchEvent) {
      if (isPullRefreshOptOut(pullTarget(e))) { startY.current = 0; return }
      if (window.scrollY !== 0) { startY.current = 0; return }
      const y = e.touches[0]?.clientY ?? 0
      if (!y) { startY.current = 0; return }
      // Terminal page: the pull must start above the sessions tabs.
      if (!isTerminalPullAllowed(y)) { startY.current = 0; return }
      startY.current = y
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
        if (delta.current > threshold()) window.location.reload()
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
