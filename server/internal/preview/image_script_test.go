package preview

import (
	"bytes"
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
			if hdr.Mode&0o111 == 0 || !bytes.Contains(body.Bytes(), []byte("export DISPLAY")) {
				t.Fatal("browser startup script must be executable and export DISPLAY")
			}
			return
		}
	}
}
