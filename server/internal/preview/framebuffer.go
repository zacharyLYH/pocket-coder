package preview

import "math"

// The sidecar's X screen IS the VNC framebuffer: x11vnc encodes it and the
// noVNC client allocates it. A fixed 1920x1080 screen wastes most of that on
// a phone (a 390x844 CSS viewport is 16% of those pixels), so the screen is
// derived from the client's CSS viewport and DPR, bounded by a pixel budget
// so a 4K desktop or a 3x retina phone cannot blow up sidecar memory.
const (
	// MaxDeviceScaleFactor caps retina sharpness: at 2x a 390x844 CSS viewport
	// renders 780x1688 physical pixels (64% of the old fixed framebuffer and
	// 1.5x rather than 3x upscaling on a phone). 3x would cost 143% of the old
	// framebuffer for a difference no preview can show.
	MaxDeviceScaleFactor = 2
	// MaxFramebufferPixels bounds the X screen at ~16 MB (4 B/px). DPR steps
	// down before this ever shrinks a viewport that fits at 1x.
	MaxFramebufferPixels = 4_000_000
	// MinViewport is the smallest viewport that is treated as real: anything
	// smaller (including the zero value from callers that predate viewport
	// reporting) falls back to the legacy full-size screen.
	MinViewport = 100
)

// ClampDPR folds a client-reported device pixel ratio into the supported
// range. Absent (0) and negative values mean 1.
func ClampDPR(dpr int) int {
	if dpr < 1 {
		return 1
	}
	if dpr > MaxDeviceScaleFactor {
		return MaxDeviceScaleFactor
	}
	return dpr
}

// FramebufferSize returns the X screen size for a client viewport. A missing
// or nonsense viewport (no client to size from) keeps the legacy
// DisplayWidth x DisplayHeight screen.
func FramebufferSize(viewWidth, viewHeight, dpr int) (int, int) {
	if viewWidth < MinViewport || viewHeight < MinViewport {
		return DisplayWidth, DisplayHeight
	}
	dpr = ClampDPR(dpr)
	width, height := viewWidth*dpr, viewHeight*dpr
	// Step the DPR down before shrinking the viewport itself: a 1080p retina
	// client keeps its 1x size instead of ending up smaller than it asked for.
	for dpr > 1 && width*height > MaxFramebufferPixels {
		dpr--
		width, height = viewWidth*dpr, viewHeight*dpr
	}
	// Only a viewport too large even at 1x gets scaled down, aspect preserved.
	if px := width * height; px > MaxFramebufferPixels {
		factor := math.Sqrt(float64(MaxFramebufferPixels) / float64(px))
		width, height = int(float64(width)*factor), int(float64(height)*factor)
	}
	return width, height
}

// FitViewport clamps a CSS viewport (and its DPR) so the Chromium window it
// describes — width*dpr by height*dpr physical pixels — fits the framebuffer.
// The X screen is the hard ceiling (no window manager can grow it), and the
// noVNC client scales whatever does not fill it.
func FitViewport(cssWidth, cssHeight, dpr, fbWidth, fbHeight int) (int, int) {
	dpr = ClampDPR(dpr)
	maxWidth := max(fbWidth/dpr, MinViewport)
	maxHeight := max(fbHeight/dpr, MinViewport)
	return max(min(cssWidth, maxWidth), MinViewport), max(min(cssHeight, maxHeight), MinViewport)
}
