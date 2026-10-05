import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest'
import { render, screen, act } from '@testing-library/react'

import { usePullToRefresh, isPullRefreshOptOut } from '@/hooks/usePullToRefresh'

function Probe() {
  const pulling = usePullToRefresh()
  return (
    <>
      <div data-testid="pull-state">{pulling ? 'pulling' : 'idle'}</div>
      <div data-testid="fab-stub" data-no-pull-refresh>fab</div>
    </>
  )
}

// jsdom has no TouchEvent/Touch constructors and no ontouchstart — the hook
// early-returns without it. Fake the minimal touch surface the hook reads:
// e.touches[0].clientY (+ changedTouches fallback), bubbling to document.
function touchOn(el: Element, type: 'touchstart' | 'touchmove' | 'touchend', y: number) {
  const ev = new Event(type, { bubbles: true, cancelable: true }) as Event & {
    touches: Array<{ clientY: number; target: Element }>
    changedTouches: Array<{ clientY: number; target: Element }>
  }
  const touch = { clientY: y, target: el }
  if (type === 'touchend') {
    ev.touches = []
    ev.changedTouches = [touch]
  } else {
    ev.touches = [touch]
    ev.changedTouches = [touch]
  }
  act(() => { el.dispatchEvent(ev) })
}

describe('isPullRefreshOptOut', () => {
  it('opts out the FAB and its children, nothing else', () => {
    render(<Probe />)
    const fab = screen.getByTestId('fab-stub')
    expect(isPullRefreshOptOut(fab)).toBe(true)
    expect(isPullRefreshOptOut(document.body)).toBe(false)
    expect(isPullRefreshOptOut(null)).toBe(false)
  })
})

describe('usePullToRefresh + FAB edge case', () => {
  let reload: ReturnType<typeof vi.fn>

  beforeEach(() => {
    // Enable the hook's touch path (absent in jsdom by default).
    ;(window as unknown as Record<string, unknown>).ontouchstart = () => {}
    Object.defineProperty(window, 'scrollY', { configurable: true, value: 0 })
    reload = vi.fn()
    // jsdom Location.reload is non-writable — replace the whole location
    // object shape the hook touches (only .reload).
    Object.defineProperty(window, 'location', { configurable: true, value: { reload } })
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    delete (window as unknown as Record<string, unknown>).ontouchstart
  })

  it('ignores short pulls, reloads on a 1/3-viewport drag (baseline)', () => {
    render(<Probe />)
    touchOn(document.body, 'touchstart', 80)
    touchOn(document.body, 'touchmove', 140)
    touchOn(document.body, 'touchmove', 220)
    expect(screen.getByTestId('pull-state')).toHaveTextContent('pulling')
    touchOn(document.body, 'touchend', 220)
    // 140px < innerHeight/3 in jsdom (768px) → no reload: casual scrolls
    // and short flicks never trigger it.
    expect(reload).not.toHaveBeenCalled()
    touchOn(document.body, 'touchstart', 80)
    touchOn(document.body, 'touchmove', 220)
    touchOn(document.body, 'touchmove', 420)
    expect(screen.getByTestId('pull-state')).toHaveTextContent('pulling')
    touchOn(document.body, 'touchend', 420)
    expect(reload).toHaveBeenCalledTimes(1)
  })

  it('dragging down past 1/3 viewport starting on the FAB never refreshes', () => {
    render(<Probe />)
    const fab = screen.getByTestId('fab-stub')
    // The reported bug: FAB sits at the top, user drags it down 300px+
    // (past innerHeight/3) at scrollY 0 → page reloaded.
    touchOn(fab, 'touchstart', 40)
    touchOn(fab, 'touchmove', 200)
    touchOn(fab, 'touchmove', 400)
    // No spinner while dragging from the FAB …
    expect(screen.getByTestId('pull-state')).toHaveTextContent('idle')
    touchOn(fab, 'touchend', 400)
    // … and no reload on release.
    expect(reload).not.toHaveBeenCalled()
  })

  it('a FAB touch mid-sequence aborts an in-flight body pull', () => {
    render(<Probe />)
    const fab = screen.getByTestId('fab-stub')
    touchOn(document.body, 'touchstart', 80)
    touchOn(document.body, 'touchmove', 110)
    expect(screen.getByTestId('pull-state')).toHaveTextContent('pulling')
    // Finger slides onto the FAB (touch retargets): pull must cancel.
    touchOn(fab, 'touchmove', 200)
    expect(screen.getByTestId('pull-state')).toHaveTextContent('idle')
    touchOn(fab, 'touchend', 200)
    expect(reload).not.toHaveBeenCalled()
  })
})
