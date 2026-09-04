package preview

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"io"

	"sps/internal/docker"
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
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	files := []struct {
		name string
		mode int64
		data []byte
	}{
		{"Dockerfile", 0o644, browserDockerfile},
		{"start-browser", 0o755, browserStartScript},
	}
	for _, file := range files {
		_ = tw.WriteHeader(&tar.Header{Name: file.name, Mode: file.mode, Size: int64(len(file.data))})
		_, _ = tw.Write(file.data)
	}
	_ = tw.Close()
	return &buf
}
