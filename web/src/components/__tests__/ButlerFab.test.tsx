import { describe, expect, it, vi } from 'vitest'


import { render, screen, fireEvent } from '@testing-library/react'
import { ButlerFab, snapToCorner, FAB_SIZE, SNAP_MARGIN, TOP_CLEARANCE } from '@/components/ButlerFab'

// jsdom has no layout: fix a phone-ish viewport so snapping is deterministic.
function phoneViewport() {
  Object.defineProperty(window, 'innerWidth', { configurable: true, value: 390 })
  Object.defineProperty(window, 'innerHeight', { configurable: true, value: 844 })
}

function drag(el: HTMLElement, from: { x: number; y: number }, to: { x: number; y: number }, steps = 5) {
  fireEvent.pointerDown(el, { pointerId: 1, clientX: from.x, clientY: from.y })
  for (let i = 1; i <= steps; i++) {
    const x = from.x + ((to.x - from.x) * i) / steps
    const y = from.y + ((to.y - from.y) * i) / steps
    fireEvent.pointerMove(el, { pointerId: 1, clientX: x, clientY: y })
  }
}

describe('snapToCorner', () => {
  it('snaps bottom-left coords to bottom-left corner', () => {
    // vh=844, vw=390: bottom offset small = near bottom; right offset LARGE
    // = far from the right edge = near the LEFT edge.
    expect(snapToCorner(20, 326, 844, 390)).toEqual({ bottom: SNAP_MARGIN, right: 390 - FAB_SIZE - SNAP_MARGIN })
    expect(snapToCorner(20, 330, 844, 390)).toEqual({ bottom: SNAP_MARGIN, right: 390 - FAB_SIZE - SNAP_MARGIN })
  })

  it('snaps top-right coords to top-right corner below the notch', () => {
    // Top = large bottom offset; right edge = SMALL right offset. The top
    // snap stops TOP_CLEARANCE below the viewport top, clear of the
    // notification-shade swipe zone.
    expect(snapToCorner(756, 16, 844, 390)).toEqual({ bottom: 844 - FAB_SIZE - TOP_CLEARANCE, right: SNAP_MARGIN })
    expect(snapToCorner(780, 20, 844, 390)).toEqual({ bottom: 844 - FAB_SIZE - TOP_CLEARANCE, right: SNAP_MARGIN })
  })
})

describe('ButlerFab drag', () => {
  it('drags from bottom-left to top-right and snaps there', () => {
    phoneViewport()
    render(<ButlerFab projectHint={null} />)
    const fab = screen.getByTestId('butler-fab')

    // Starts bottom-right.
    expect(fab.style.bottom).toBe('20px')
    expect(fab.style.right).toBe('20px')

    // bottom-right -> bottom-left: dx negative (move left), dy ~0.
    // right offset grows (far from right edge = near left edge).
    drag(fab, { x: 350, y: 800 }, { x: 40, y: 780 })
    fireEvent.pointerUp(fab, { pointerId: 1, clientX: 40, clientY: 780 })
    expect(fab.style.bottom).toBe(`${SNAP_MARGIN}px`)
    expect(fab.style.right).toBe(`${390 - FAB_SIZE - SNAP_MARGIN}px`)
    // A drag must not open the sheet (the trailing click is suppressed).
    fireEvent.click(fab)
    expect(screen.queryByTestId('butler-sheet')).not.toBeInTheDocument()

    // Dragging must not touch localStorage (dependency removed): spy and
    // confirm no reads/writes for our old key happen during the drag.
    const getSpy = vi.spyOn(Storage.prototype, 'getItem')
    const setSpy = vi.spyOn(Storage.prototype, 'setItem')
    drag(fab, { x: 40, y: 780 }, { x: 350, y: 40 })
    fireEvent.pointerUp(fab, { pointerId: 1, clientX: 350, clientY: 40 })
    expect(fab.style.bottom).toBe(`${844 - FAB_SIZE - TOP_CLEARANCE}px`)
    expect(fab.style.right).toBe(`${SNAP_MARGIN}px`)
    expect(getSpy.mock.calls.some((c) => c[0] === 'butlerFabPos')).toBe(false)
    expect(setSpy.mock.calls.some((c) => c[0] === 'butlerFabPos')).toBe(false)
    getSpy.mockRestore()
    setSpy.mockRestore()
    fireEvent.click(fab)
    expect(screen.queryByTestId('butler-sheet')).not.toBeInTheDocument()
  })

  it('a plain click still opens the sheet', () => {
    phoneViewport()
    render(<ButlerFab projectHint={null} />)
    const fab = screen.getByTestId('butler-fab')
    fireEvent.pointerDown(fab, { pointerId: 1, clientX: 350, clientY: 800 })
    fireEvent.pointerUp(fab, { pointerId: 1, clientX: 350, clientY: 800 })
    fireEvent.click(fab)
    expect(screen.getByTestId('butler-sheet')).toBeInTheDocument()
  })

  it('pointer cancel drops the drag without moving or opening', () => {
    phoneViewport()
    render(<ButlerFab projectHint={null} />)
    const fab = screen.getByTestId('butler-fab')
    fireEvent.pointerDown(fab, { pointerId: 1, clientX: 350, clientY: 800 })
    fireEvent.pointerMove(fab, { pointerId: 1, clientX: 200, clientY: 400 })
    fireEvent.pointerCancel(fab)
    expect(fab.style.bottom).toBe('20px')
    expect(fab.style.right).toBe('20px')
  })
})
