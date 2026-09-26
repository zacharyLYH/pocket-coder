package project

import (
	_ "embed"
	"io"

	"pcoder/internal/docker"
)

//go:embed image/Dockerfile
var projectDockerfile []byte

//go:embed image/pcoder-update-runtime
var projectUpdateScript []byte

// projectContext returns a tar stream of the embedded project build context,
// so the image definition ships inside the binary and no path configuration
// is needed.
func projectContext() io.Reader {
	return docker.TarFiles(
		docker.TarFile{Name: "Dockerfile", Mode: 0o644, Data: projectDockerfile},
		docker.TarFile{Name: "pcoder-update-runtime", Mode: 0o644, Data: projectUpdateScript},
	)
}
