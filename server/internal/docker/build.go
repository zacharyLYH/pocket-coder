package docker

import (
	"context"
	"fmt"
	"io"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// BuildOptions describe an image build.
type BuildOptions struct {
	Tag        string // resulting image name:tag
	ContextDir string // build context directory ("" when InputStream is set)
	Dockerfile string // path relative to ContextDir ("" = Dockerfile)
	// InputStream, when set, is a tar stream used as the build context
	// instead of ContextDir (e.g. the embedded project definition).
	InputStream io.Reader
}

// Build builds an image, streaming buildkit progress to log.
func (d *Docker) Build(ctx context.Context, opts BuildOptions, log io.Writer) error {
	if err := d.c.BuildImage(dockerclient.BuildImageOptions{
		Context:      ctx,
		Name:         opts.Tag,
		ContextDir:   opts.ContextDir,
		Dockerfile:   opts.Dockerfile,
		InputStream:  opts.InputStream,
		OutputStream: log,
	}); err != nil {
		return fmt.Errorf("build %s: %w", opts.Tag, err)
	}
	return nil
}
