package preview

import (
	"archive/tar"
	"bytes"
	"io"
	"strings"
	"testing"
)

// browserContextFiles reads the in-memory Docker build context into a map, so
// tests assert on exactly what the image gets (not just on files on disk).
func browserContextFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	tr := tar.NewReader(browserContext())
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		files[hdr.Name] = body
	}
}

func TestBrowserContextIncludesExecutableDisplayScript(t *testing.T) {
	files := browserContextFiles(t)
	script, ok := files["start-browser"]
	if !ok {
		t.Fatal("browser build context is missing start-browser")
	}
	if !bytes.Contains(script, []byte("export DISPLAY")) {
		t.Fatal("browser startup script must export DISPLAY")
	}
	// The tar entry's mode is what Docker chmods; assert it here because the
	// build context is the only artifact Docker sees.
	tr := tar.NewReader(browserContext())
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatal(err)
		}
		if hdr.Name == "start-browser" && hdr.Mode&0o111 == 0 {
			t.Fatal("start-browser must be executable")
		}
		if hdr.Name == "start-browser" {
			break
		}
	}
	// One resizable X server, not the old Xvfb + x11vnc pair: Xvfb has no
	// RandR resize, which is the whole reason for the swap.
	if !bytes.Contains(script, []byte("Xtigervnc")) {
		t.Fatal("start-browser must start Xtigervnc")
	}
	// Comments may name the old stack; executable lines may not. Maximizing
	// is a no-op without a window manager, so that flag must not come back
	// either (the CDP window bounds are what fill the desktop).
	for _, line := range strings.Split(string(script), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, dead := range []string{"Xvfb", "x11vnc", "start-maximized"} {
			if strings.Contains(trimmed, dead) {
				t.Errorf("start-browser must not use %q: %q", dead, trimmed)
			}
		}
	}
	if !bytes.Contains(script, []byte(`--window-size="${WIDTH},${HEIGHT}"`)) {
		t.Fatal("start-browser must seed the Chromium window from the desktop env")
	}
}

// The entry page is part of the image: it must ask noVNC to resize the remote
// desktop, and its imports must resolve beside noVNC's core/ directory.
func TestBrowserContextShipsResizeAwareEntryPage(t *testing.T) {
	files := browserContextFiles(t)
	page, ok := files["pcoder.html"]
	if !ok {
		t.Fatal("browser build context is missing pcoder.html")
	}
	if !bytes.Contains(page, []byte("resizeSession")) {
		t.Fatal("pcoder.html must enable rfb.resizeSession")
	}
	if !bytes.Contains(page, []byte("./core/rfb.js")) {
		t.Fatal("pcoder.html must import noVNC relatively so the cookie gate applies")
	}
	// The comment may name the dropped bar; the code must not carry it.
	for _, trimmed := range []string{"sendCtrlAltDel", "top_bar"} {
		if bytes.Contains(page, []byte(trimmed)) {
			t.Fatalf("pcoder.html must drop noVNC's CtrlAltDel bar, found %q", trimmed)
		}
	}
	dockerfile := string(files["Dockerfile"])
	if !strings.Contains(dockerfile, "tigervnc-standalone-server") {
		t.Fatal("Dockerfile must install tigervnc-standalone-server")
	}
	if strings.Contains(dockerfile, "x11vnc") || strings.Contains(dockerfile, "xvfb") {
		t.Fatal("Dockerfile still installs the fixed-screen X stack")
	}
	if !strings.Contains(dockerfile, "pcoder.html /usr/share/novnc/pcoder.html") {
		t.Fatal("Dockerfile must copy the entry page into noVNC's web root")
	}
}
