package project

import (
	"archive/tar"
	"bytes"
	_ "embed"
	"io"
)

//go:embed image/Dockerfile
var projectDockerfile []byte

//go:embed image/pcoder-update-runtime
var projectUpdateScript []byte

// projectContext returns a tar stream of the embedded project build context,
// so the image definition ships inside the binary and no path configuration
// is needed. Writes go to a bytes.Buffer, which cannot fail, so errors are
// ignored.
func projectContext() io.Reader {
	files := map[string][]byte{
		"Dockerfile":            projectDockerfile,
		"pcoder-update-runtime": projectUpdateScript,
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
