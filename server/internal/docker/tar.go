package docker

import (
	"archive/tar"
	"bytes"
	"io"
)

// TarFile is one entry in a build-context tar stream.
type TarFile struct {
	Name string
	Mode int64
	Data []byte
}

// TarFiles packs entries into a tar stream for a docker build context.
// Writes go to a bytes.Buffer, which cannot fail, so errors are ignored.
func TarFiles(files ...TarFile) io.Reader {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Name: f.Name, Mode: f.Mode, Size: int64(len(f.Data))})
		_, _ = tw.Write(f.Data)
	}
	_ = tw.Close()
	return &buf
}
