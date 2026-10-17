package preview

import "testing"

// FramebufferSize is only a seed now: the desktop starts at the client's 1x
// viewport and resizes live afterwards. It must ignore any DPR notion, bound
// a huge seed, and keep the legacy screen for callers without a viewport.
func TestFramebufferSeedFollowsClientViewportAt1x(t *testing.T) {
	cases := []struct {
		name                  string
		viewWidth, viewHeight int
		wantWidth, wantHeight int
	}{
		{"phone", 390, 844, 390, 844},
		{"desktop", 1280, 720, 1280, 720},
		// Absent viewport keeps the legacy screen (old clients, tool calls).
		{"no viewport", 0, 0, DisplayWidth, DisplayHeight},
		{"junk viewport", 10, 10, DisplayWidth, DisplayHeight},
		// A seed that busts the budget even at 1x is scaled down, aspect kept.
		{"larger than budget", 3000, 2000, 2449, 1632},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotW, gotH := FramebufferSize(tc.viewWidth, tc.viewHeight)
			if gotW != tc.wantWidth || gotH != tc.wantHeight {
				t.Fatalf("FramebufferSize(%d, %d) = %dx%d, want %dx%d",
					tc.viewWidth, tc.viewHeight, gotW, gotH, tc.wantWidth, tc.wantHeight)
			}
			// The budget is the seed-memory guarantee, so assert it for every case.
			if px := gotW * gotH; px > MaxFramebufferPixels {
				t.Fatalf("%dx%d = %d px exceeds the %d px budget", gotW, gotH, px, MaxFramebufferPixels)
			}
		})
	}
}
