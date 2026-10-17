package preview

import (
	"bytes"
	"strings"
	"testing"

	"archive/tar"
)

func TestBrowserContextIncludesExecutableDisplayScript(t *testing.T) {
	tr := tar.NewReader(browserContext())
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "start-browser" {
			body := new(bytes.Buffer)
			_, _ = body.ReadFrom(tr)
			script := body.Bytes()
			if hdr.Mode&0o111 == 0 || !bytes.Contains(script, []byte("export DISPLAY")) {
				t.Fatal("browser startup script must be executable and export DISPLAY")
			}
			// Maximizing is a no-op under the WM-less Xvfb, so the flag must not
			// come back (comments explaining why are fine).
			for _, line := range strings.Split(string(script), "\n") {
				if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, "start-maximized") {
					t.Fatalf("start-browser must not request maximizing (no WM honors it): %q", trimmed)
				}
			}
			if !bytes.Contains(script, []byte(`--window-size="${WIDTH},${HEIGHT}"`)) {
				t.Fatal("start-browser must size the window from the framebuffer env")
			}
			return
		}
	}
}
