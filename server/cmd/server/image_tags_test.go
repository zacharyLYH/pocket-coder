package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pcoder/internal/preview"
	"pcoder/internal/project"
)

// deploy/setup.sh prebuilds the on-demand runtime images so the first clone or
// preview does not pay a multi-minute Docker build inside its HTTP request.
// Its tags are shell literals while the truth lives in Go, and a stale one
// silently restores that stall without failing anything: the server just
// builds the missing tag in-request. It has drifted twice (the project image
// by a whole version, script v3 while the constant said v4; the browser image
// on a version bump), so pin them here.
func TestSetupScriptPrebuildsCurrentImageTags(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		path := filepath.Join(dir, "deploy", "setup.sh")
		if script, err := os.ReadFile(path); err == nil {
			checkPrebuiltTags(t, string(script))
			return
		}
		if parent := filepath.Dir(dir); parent != dir {
			dir = parent
			continue
		}
		t.Fatal("deploy/setup.sh not found above the test's cwd")
	}
}

func checkPrebuiltTags(t *testing.T, script string) {
	t.Helper()
	for _, tag := range []string{project.ProjectImage, preview.DefaultBrowserImage} {
		if !strings.Contains(script, "-t "+tag+" ") {
			t.Fatalf("deploy/setup.sh does not prebuild %s — the first use of that image would build it inside a request", tag)
		}
	}
}
