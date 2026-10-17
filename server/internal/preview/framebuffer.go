package preview

import "math"

// The sidecar's desktop IS the VNC framebuffer: Xtigervnc encodes it and the
// noVNC client allocates it. Xtigervnc can resize the desktop at runtime (the
// client asks through resizeSession), so the size below is only a seed — the
// desktop the sidecar starts at before the first client syncs it. Sizing it
// from the first client's viewport saves a resize on open; the desktop would
// converge without it.
const (
	// MaxFramebufferPixels bounds the seed desktop at ~16 MB (4 B/px), so a
	// 4K viewport cannot blow up sidecar memory before the first resize.
	MaxFramebufferPixels = 4_000_000
	// MinViewport is the smallest viewport that is treated as real: anything
	// smaller (including the zero value from callers that predate viewport
	// reporting) falls back to the legacy full-size screen.
	MinViewport = 100
)

// FramebufferSize returns the seed desktop size for a client viewport at 1x.
// A missing or nonsense viewport (no client to size from) keeps the legacy
// DisplayWidth x DisplayHeight screen; a viewport too large for the pixel
// budget is scaled down with its aspect preserved.
func FramebufferSize(viewWidth, viewHeight int) (int, int) {
	if viewWidth < MinViewport || viewHeight < MinViewport {
		return DisplayWidth, DisplayHeight
	}
	width, height := viewWidth, viewHeight
	if px := width * height; px > MaxFramebufferPixels {
		factor := math.Sqrt(float64(MaxFramebufferPixels) / float64(px))
		width, height = int(float64(width)*factor), int(float64(height)*factor)
	}
	return width, height
}
