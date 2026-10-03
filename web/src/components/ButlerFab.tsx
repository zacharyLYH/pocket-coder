import { useRef, useState } from 'react'
import { Bot } from 'lucide-react'
import { ButlerSheet } from '@/components/ButlerSheet'

export const FAB_SIZE = 48
export const SNAP_MARGIN = 16

// Snap to the nearest corner: threshold at the viewport center.
export function snapToCorner(bottom: number, right: number, vh: number, vw: number) {
  const halfH = vh / 2
  const halfW = vw / 2
  return {
    bottom: bottom < halfH ? SNAP_MARGIN : Math.max(SNAP_MARGIN, vh - FAB_SIZE - SNAP_MARGIN),
    right: right < halfW ? SNAP_MARGIN : Math.max(SNAP_MARGIN, vw - FAB_SIZE - SNAP_MARGIN),
  }
}

// ButlerFab: one floating button + centered floating chat card on mobile
// (margins all around, tap outside to exit), floating chat window on
// desktop (old-messenger vibe). Same component on Home and in the project
// header; the project id rides as a clearable hint chip.
//
// The FAB is draggable via pointer events — grab it and fling it toward a
// corner and it snaps into place on release. The position is intentionally
// in-memory only (no localStorage): every load starts bottom-right, so two
// devices/tabs can never disagree and private-mode storage quirks can't
// break the button.
export function ButlerFab({ projectHint }: { projectHint?: string | null }) {
  const [open, setOpen] = useState(false)
  const [hintCleared, setHintCleared] = useState(false)
  const hint = projectHint && !hintCleared ? projectHint : null
  const [pos, setPos] = useState({ bottom: 20, right: 20 })
  const [delta, setDelta] = useState({ x: 0, y: 0 })
  const btnRef = useRef<HTMLDivElement>(null)
  const start = useRef<{ x: number; y: number } | null>(null)
  const startPos = useRef<{ bottom: number; right: number }>({ bottom: 0, right: 0 })
  const deltaRef = useRef({ x: 0, y: 0 })
  const moved = useRef(false)
  const posRef = useRef(pos)
  posRef.current = pos

  function handlePointerDown(e: React.PointerEvent) {
    try { btnRef.current?.setPointerCapture(e.pointerId) } catch { /* jsdom / no capture */ }
    start.current = { x: e.clientX, y: e.clientY }
    startPos.current = { ...posRef.current }
    deltaRef.current = { x: 0, y: 0 }
    moved.current = false
  }

  function handlePointerMove(e: React.PointerEvent) {
    if (!start.current) return
    // With pointer capture the move targets the FAB even off-element;
    // without it (touch cancel) start is cleared so this is a no-op.
    const dx = e.clientX - start.current.x
    const dy = e.clientY - start.current.y
    if (Math.abs(dx) + Math.abs(dy) > 5) moved.current = true
    deltaRef.current = { x: dx, y: dy }
    setDelta({ x: dx, y: dy })
  }

  function endDrag(e: React.PointerEvent) {
    if (!start.current) return
    try { btnRef.current?.releasePointerCapture(e.pointerId) } catch { /* ignore */ }
    // deltaRef has the latest value from handlePointerMove — closures
    // in pointer handlers always see the current ref, unlike state.
    const snapped = snapToCorner(
      startPos.current.bottom - deltaRef.current.y,
      startPos.current.right - deltaRef.current.x,
      window.innerHeight,
      window.innerWidth,
    )
    posRef.current = snapped
    setPos(snapped)
    setDelta({ x: 0, y: 0 })
    deltaRef.current = { x: 0, y: 0 }
    start.current = null
    // Leave moved.current as-is so the post-drag click doesn't toggle Butler.
    // It resets on the next pointerDown.
  }

  function handlePointerCancel() {
    // Touch scroll takeover / gesture abort: drop the drag, snap back to
    // where it started instead of sticking mid-drag.
    start.current = null
    deltaRef.current = { x: 0, y: 0 }
    setDelta({ x: 0, y: 0 })
  }

  return (
    <>
      <div
        ref={btnRef}
        onPointerDown={handlePointerDown}
        onPointerMove={handlePointerMove}
        onPointerUp={endDrag}
        onPointerCancel={handlePointerCancel}
        onClick={() => { if (!moved.current) { setOpen((o) => !o); setHintCleared(false) } }}
        data-testid="butler-fab"
        aria-label="Open Butler"
        data-no-pull-refresh
        className="fixed z-50 flex size-12 cursor-grab touch-none items-center justify-center rounded-full bg-background text-foreground shadow-lg select-none hover:bg-muted active:cursor-grabbing"
        style={{
          bottom: pos.bottom,
          right: pos.right,
          transform: `translate(${delta.x}px, ${delta.y}px)`,
          touchAction: 'none',
        }}
      >
        <Bot className="size-5" />
      </div>
      {open && (
        <div className="fixed inset-0 z-50" data-testid="butler-overlay" data-no-pull-refresh>
          <div className="absolute inset-0 bg-black/40 backdrop-blur" onClick={() => setOpen(false)} data-testid="butler-backdrop" />
          <div className="absolute inset-x-4 top-16 bottom-16 overflow-hidden rounded-2xl border bg-background shadow-xl sm:inset-x-auto sm:top-auto sm:right-5 sm:bottom-24 sm:left-auto sm:h-[540px] sm:max-h-[calc(100dvh-8rem)] sm:w-[380px] sm:rounded-2xl sm:border sm:pointer-events-auto sm:shadow-2xl" role="dialog" aria-label="Butler">
            <ButlerSheet projectHint={hint} onClearHint={() => setHintCleared(true)} onClose={() => setOpen(false)} />
          </div>
        </div>
      )}
    </>
  )
}
