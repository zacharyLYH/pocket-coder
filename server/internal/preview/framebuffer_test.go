package preview

import "testing"

func TestFramebufferSizeFollowsClientViewport(t *testing.T) {
	cases := []struct {
		name                       string
		viewWidth, viewHeight, dpr int
		wantWidth, wantHeight      int
	}{
		// A phone: 390x844 CSS at 2x DPR is 1.32M px, 64% of the old fixed
		// 1920x1080 screen — the whole point of the change.
		{"phone retina", 390, 844, 2, 780, 1688},
		// 3x is capped: 3x would be 2.97M px, more than the 1920x1080 it replaces.
		{"phone 3x capped", 390, 844, 3, 780, 1688},
		// DPR 1 is the raw viewport (e2e and non-retina desktops).
		{"phone dpr1", 390, 844, 1, 390, 844},
		{"desktop", 1280, 720, 1, 1280, 720},
		// 1280x720 at 2x is 3.69M px, inside the budget.
		{"desktop retina", 1280, 720, 2, 2560, 1440},
		// 1920x1080 at 2x would be 8.3M px: step the DPR down rather than
		// shrink past what the client needs.
		{"1080p retina steps dpr down", 1920, 1080, 2, 1920, 1080},
		// Absent viewport keeps the legacy screen (old clients, tool calls).
		{"no viewport", 0, 0, 0, DisplayWidth, DisplayHeight},
		{"junk viewport", 10, 10, 2, DisplayWidth, DisplayHeight},
		// A viewport that busts the budget even at 1x is scaled down, aspect kept.
		{"larger than budget", 3000, 2000, 1, 2449, 1632},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := FramebufferSize(tc.viewWidth, tc.viewHeight, tc.dpr)
			if gotW != tc.wantWidth || gotH != tc.wantHeight {
				t.Fatalf("FramebufferSize(%d, %d, %d) = %dx%d, want %dx%d",
					tc.viewWidth, tc.viewHeight, tc.dpr, gotW, gotH, tc.wantWidth, tc.wantHeight)
			}
			// The budget is the memory guarantee, so assert it for every case.
			if px := gotW * gotH; px > MaxFramebufferPixels {
				t.Fatalf("%dx%d = %d px exceeds the %d px budget", gotW, gotH, px, MaxFramebufferPixels)
			}
		})
	}
}

// The perf claim, stated as a test: a phone-sized client must cost
// meaningfully fewer pixels per frame than the old fixed screen. This is the
// number the e2e framebuffer spec re-measures against a real sidecar.
func TestFramebufferSizeCutsPhonePixelsAgainstLegacyScreen(t *testing.T) {
	width, height := FramebufferSize(390, 844, 3)
	got := float64(width * height)
	legacy := float64(DisplayWidth * DisplayHeight)
	if ratio := got / legacy; ratio > 0.70 {
		t.Fatalf("phone framebuffer is %.0f%% of the legacy screen, want <= 70%%", ratio*100)
	}
	// And it must never be bigger than what the client renders.
	if want := 390 * 844 * MaxDeviceScaleFactor * MaxDeviceScaleFactor; width*height > want {
		t.Fatalf("%dx%d exceeds the %d px the client can actually display", width, height, want)
	}
}

func TestClampDPR(t *testing.T) {
	for _, tc := range []struct{ in, want int }{
		{0, 1}, {-3, 1}, {1, 1}, {2, 2}, {3, MaxDeviceScaleFactor}, {99, MaxDeviceScaleFactor},
	} {
		if got := ClampDPR(tc.in); got != tc.want {
			t.Fatalf("ClampDPR(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestFitViewportClampsToFramebufferAtScale(t *testing.T) {
	cases := []struct {
		name                      string
		cssW, cssH, dpr, fbW, fbH int
		wantW, wantH              int
	}{
		// Exact fit: the phone that sized this framebuffer.
		{"exact", 390, 844, 2, 780, 1688, 390, 844},
		// A bigger ask is clamped to fb/dpr so the window never exceeds the screen.
		{"oversize", 3000, 2000, 1, 1280, 720, 1280, 720},
		{"oversize retina", 900, 900, 2, 780, 1688, 390, 844},
		// A DPR the sidecar does not support is folded, so the clamp uses dpr 2.
		{"dpr folded", 900, 900, 3, 780, 1688, 390, 844},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := FitViewport(tc.cssW, tc.cssH, tc.dpr, tc.fbW, tc.fbH)
			if gotW != tc.wantW || gotH != tc.wantH {
				t.Fatalf("FitViewport(%d, %d, %d, %d, %d) = %dx%d, want %dx%d",
					tc.cssW, tc.cssH, tc.dpr, tc.fbW, tc.fbH, gotW, gotH, tc.wantW, tc.wantH)
			}
			// Whatever comes out, the physical window must fit the screen.
			if w, h := gotW*ClampDPR(tc.dpr), gotH*ClampDPR(tc.dpr); w > tc.fbW || h > tc.fbH {
				t.Fatalf("window %dx%d does not fit framebuffer %dx%d", w, h, tc.fbW, tc.fbH)
			}
		})
	}
}
