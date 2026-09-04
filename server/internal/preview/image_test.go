package preview

import (
	"context"
	"errors"
	"io"
	"testing"

	"sps/internal/docker"
)

type imageFake struct {
	inspectErr error
	built      bool
}

func (f *imageFake) InspectImage(context.Context, string) error { return f.inspectErr }
func (f *imageFake) Build(_ context.Context, opts docker.BuildOptions, _ io.Writer) error {
	f.built = opts.Tag == DefaultBrowserImage && opts.InputStream != nil
	return nil
}

func TestEnsureBrowserImageBuildsOnlyWhenMissing(t *testing.T) {
	f := &imageFake{inspectErr: errors.New("missing")}
	// A non-sentinel error must not trigger a build.
	if err := ensureBrowserImage(context.Background(), f, DefaultBrowserImage); err == nil || f.built {
		t.Fatal("unexpected build for unavailable image error")
	}
	f.inspectErr = docker.ErrNotFound
	if err := ensureBrowserImage(context.Background(), f, DefaultBrowserImage); err != nil || !f.built {
		t.Fatalf("missing image: err=%v built=%v", err, f.built)
	}
	f.built = false
	f.inspectErr = nil
	if err := ensureBrowserImage(context.Background(), f, DefaultBrowserImage); err != nil || f.built {
		t.Fatalf("existing image: err=%v built=%v", err, f.built)
	}
}
