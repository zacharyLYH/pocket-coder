package project

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"io"
)

//go:embed sandbox/Dockerfile
var sandboxDockerfile []byte

//go:embed sandbox/sps-update-runtime
var sandboxUpdateScript []byte

// sandboxContext returns a tar stream of the embedded sandbox build context,
// so the image definition ships inside the binary and no path configuration
// is needed. Writes go to a bytes.Buffer, which cannot fail, so errors are
// ignored.
func sandboxContext() io.Reader {
	files := map[string][]byte{
		"Dockerfile":         sandboxDockerfile,
		"sps-update-runtime": sandboxUpdateScript,
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, raw := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(raw))})
		_, _ = tw.Write(raw)
	}
	_ = tw.Close()
	return &buf
}
