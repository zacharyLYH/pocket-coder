package preview

import (
	"context"
	_ "embed"
	"errors"
	"io"

	"pcoder/internal/docker"
)

//go:embed image/Dockerfile
var browserDockerfile []byte

//go:embed image/start-browser
var browserStartScript []byte

type imageRuntime interface {
	InspectImage(context.Context, string) error
	Build(context.Context, docker.BuildOptions, io.Writer) error
}

func ensureBrowserImage(ctx context.Context, d imageRuntime, image string) error {
	if err := d.InspectImage(ctx, image); err == nil {
		return nil
	} else if !errors.Is(err, docker.ErrNotFound) {
		return err
	}
	return d.Build(ctx, docker.BuildOptions{Tag: image, InputStream: browserContext()}, io.Discard)
}

func browserContext() io.Reader {
	return docker.TarFiles(
		docker.TarFile{Name: "Dockerfile", Mode: 0o644, Data: browserDockerfile},
		docker.TarFile{Name: "start-browser", Mode: 0o755, Data: browserStartScript},
	)
}
