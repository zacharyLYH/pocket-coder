export const FAB_SIZE = 48
export const SNAP_MARGIN = 16
// Top clearance keeps the snapped FAB below the notch and out of the
// OS top-edge swipe zone (notification shade), where taps never reach it.
export const TOP_CLEARANCE = 76

// Snap to the nearest corner: threshold at the viewport center.
export function snapToCorner(bottom: number, right: number, vh: number, vw: number) {
  const halfH = vh / 2
  const halfW = vw / 2
  return {
    bottom: bottom < halfH ? SNAP_MARGIN : Math.max(SNAP_MARGIN, vh - FAB_SIZE - TOP_CLEARANCE),
    right: right < halfW ? SNAP_MARGIN : Math.max(SNAP_MARGIN, vw - FAB_SIZE - SNAP_MARGIN),
  }
}
