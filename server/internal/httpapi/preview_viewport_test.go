package httpapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// pageTargetCaller is a CDP session that answers the two lookup calls
// setWindowBounds makes, so the viewport math can be asserted end to end.
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

// The Chromium window must cover viewport x DPR physical pixels (so a 2x
// phone gets a 2x render) while the emulated layout viewport stays at the
// client's CSS size (so media queries still see a phone).
func TestApplyViewportSizesWindowToViewportTimesDPR(t *testing.T) {
	f := pageTargetCaller()
	width, height, dpr, err := applyViewport(context.Background(), f, 780, 1688, viewportRequest{Width: 390, Height: 844, DPR: 2})
	if err != nil {
		t.Fatalf("applyViewport = %v", err)
	}
	if width != 390 || height != 844 || dpr != 2 {
		t.Fatalf("applied %dx%d @%dx, want 390x844 @2x", width, height, dpr)
	}
	bounds := boundsOf(t, f)
	if bounds["width"] != 780 || bounds["height"] != 1688 {
		t.Fatalf("window bounds = %v, want 780x1688 physical pixels", bounds)
	}
	if bounds["windowState"] != "normal" {
		t.Fatalf("window state = %v, want normal (maximizing needs a WM)", bounds["windowState"])
	}
	if bodyFor(t, f, "Browser.setWindowBounds")["windowId"] != 7 {
		t.Fatalf("setWindowBounds used the wrong window: %v", bodyFor(t, f, "Browser.setWindowBounds"))
	}
	emulation := bodyFor(t, f, "Emulation.setDeviceMetricsOverride")
	if emulation["width"] != 390 || emulation["height"] != 844 || emulation["deviceScaleFactor"] != 2 {
		t.Fatalf("emulation = %v, want 390x844 @2x", emulation)
	}
}

func TestApplyViewportClampsToFramebufferAtScale(t *testing.T) {
	cases := []struct {
		name                  string
		fbWidth, fbHeight     int
		req                   viewportRequest
		wantWidth, wantHeight int
		wantDPR               int
	}{
		{
			// Exported sidecar viewport: a second client asking for more than
			// the screen gets a letterboxed clamp, never a window that spills
			// off-screen.
			name:    "oversize clamps to the screen",
			fbWidth: 1280, fbHeight: 720,
			req:       viewportRequest{Width: 3000, Height: 2000},
			wantWidth: 1280, wantHeight: 720, wantDPR: 1,
		},
		{
			name:    "oversize retina clamps at dpr",
			fbWidth: 780, fbHeight: 1688,
			req:       viewportRequest{Width: 900, Height: 900, DPR: 2},
			wantWidth: 390, wantHeight: 844, wantDPR: 2,
		},
		{
			// A 3x phone is folded to the supported maximum, not trusted.
			name:    "unsupported dpr folds to the cap",
			fbWidth: 780, fbHeight: 1688,
			req:       viewportRequest{Width: 390, Height: 844, DPR: 3},
			wantWidth: 390, wantHeight: 844, wantDPR: 2,
		},
		{
			name:    "missing dpr means 1",
			fbWidth: 1280, fbHeight: 720,
			req:       viewportRequest{Width: 800, Height: 600},
			wantWidth: 800, wantHeight: 600, wantDPR: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := pageTargetCaller()
			width, height, dpr, err := applyViewport(context.Background(), f, tc.fbWidth, tc.fbHeight, tc.req)
			if err != nil {
				t.Fatalf("applyViewport = %v", err)
			}
			if width != tc.wantWidth || height != tc.wantHeight || dpr != tc.wantDPR {
				t.Fatalf("applied %dx%d @%dx, want %dx%d @%dx",
					width, height, dpr, tc.wantWidth, tc.wantHeight, tc.wantDPR)
			}
			// Whatever comes out, the physical window fits the screen.
			bounds := boundsOf(t, f)
			if physW, _ := bounds["width"].(int); physW > tc.fbWidth {
				t.Fatalf("window width %d does not fit framebuffer %d", physW, tc.fbWidth)
			}
			if physH, _ := bounds["height"].(int); physH > tc.fbHeight {
				t.Fatalf("window height %d does not fit framebuffer %d", physH, tc.fbHeight)
			}
		})
	}
}
