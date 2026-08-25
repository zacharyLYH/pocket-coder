package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// WriteFile writes content to path inside the container — the docker cp
// primitive. Like docker cp, it PUTs to the file's directory with a tar of
// the basename, so a read-only rootfs with a writable volume on the dir
// (e.g. /root) still accepts the write.
func (d *Docker) WriteFile(ctx context.Context, id, path string, content []byte) error {
	dir, name := filepath.Split(path)
	if dir == "" {
		dir = "/"
	}
	if err := d.c.UploadToContainer(id, dockerclient.UploadToContainerOptions{
		Context: ctx, Path: dir, InputStream: tarFile(name, content),
	}); err != nil {
		return fmt.Errorf("write %s in %s: %w", path, id, err)
	}
	return nil
}

// tarFile builds a tar stream containing one regular file named name.
func tarFile(name string, content []byte) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content))})
	_, _ = tw.Write(content)
	_ = tw.Close()
	return &buf
}
