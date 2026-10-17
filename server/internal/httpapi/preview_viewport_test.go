package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// pageTargetCaller is a CDP session that answers the two lookup calls
// setWindowBounds makes, so the viewport calls can be asserted end to end.
func pageTargetCaller() *scriptedCaller {
	return &scriptedCaller{responses: map[string]json.RawMessage{
		"Target.getTargets":          json.RawMessage(`{"targetInfos":[{"targetId":"page-1","type":"page"}]}`),
		"Browser.getWindowForTarget": json.RawMessage(`{"windowId":7}`),
	}}
}

// bodyFor returns the params of the recorded call with the given method.
func bodyFor(t *testing.T, f *scriptedCaller, method string) map[string]any {
	t.Helper()
	for i, call := range f.calls {
		if strings.HasPrefix(call, method+"/") {
			return f.bodies[i]
		}
	}
	t.Fatalf("no %s call recorded; calls=%v", method, f.calls)
	return nil
}

func boundsOf(t *testing.T, f *scriptedCaller) map[string]any {
	t.Helper()
	bounds, _ := bodyFor(t, f, "Browser.setWindowBounds")["bounds"].(map[string]any)
	if bounds == nil {
		t.Fatalf("setWindowBounds had no bounds: %v", f.bodies)
	}
	return bounds
}

// The Chromium window must cover exactly the client's CSS viewport: the
// desktop resizes to the same box on the VNC side, so the frame is neither
// letterboxed nor cropped, and the layout viewport media queries see is the
// box width.
func TestApplyViewportSizesWindowToViewport(t *testing.T) {
	f := pageTargetCaller()
	width, height, err := applyViewport(context.Background(), f, viewportRequest{Width: 390, Height: 844})
	if err != nil {
		t.Fatalf("applyViewport = %v", err)
	}
	if width != 390 || height != 844 {
		t.Fatalf("applied %dx%d, want 390x844", width, height)
	}
	bounds := boundsOf(t, f)
	if bounds["width"] != 390 || bounds["height"] != 844 {
		t.Fatalf("window bounds = %v, want 390x844", bounds)
	}
	if bounds["windowState"] != "normal" {
		t.Fatalf("window state = %v, want normal (maximizing needs a WM)", bounds["windowState"])
	}
	if bodyFor(t, f, "Browser.setWindowBounds")["windowId"] != 7 {
		t.Fatalf("setWindowBounds used the wrong window: %v", bodyFor(t, f, "Browser.setWindowBounds"))
	}
	emulation := bodyFor(t, f, "Emulation.setDeviceMetricsOverride")
	if emulation["width"] != 390 || emulation["height"] != 844 || emulation["deviceScaleFactor"] != 1 {
		t.Fatalf("emulation = %v, want 390x844 @1x", emulation)
	}
	// Mobile is the caller's call: a desktop box must not emulate a phone.
	if emulation["mobile"] != false {
		t.Fatalf("emulation mobile = %v, want false", emulation["mobile"])
	}
}

// A box larger than any seed desktop is applied as-is: the desktop grows to
// match at runtime, so there is nothing to clamp against.
func TestApplyViewportDoesNotClampToASeedScreen(t *testing.T) {
	f := pageTargetCaller()
	width, height, err := applyViewport(context.Background(), f, viewportRequest{Width: 3000, Height: 2000, Mobile: true})
	if err != nil {
		t.Fatalf("applyViewport = %v", err)
	}
	if width != 3000 || height != 2000 {
		t.Fatalf("applied %dx%d, want the requested 3000x2000", width, height)
	}
	bounds := boundsOf(t, f)
	if bounds["width"] != 3000 || bounds["height"] != 2000 {
		t.Fatalf("window bounds = %v, want 3000x2000", bounds)
	}
	if emulation := bodyFor(t, f, "Emulation.setDeviceMetricsOverride"); emulation["mobile"] != true {
		t.Fatalf("emulation mobile = %v, want true", emulation["mobile"])
	}
}
