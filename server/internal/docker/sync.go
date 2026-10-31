package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// StreamExec runs cmd in the container and streams its stdout into w as it
// arrives, so large tar streams never sit in memory. stdin feeds the exec's
// standard input (nil when the command needs none). stderr is captured for
// the error message.
func (d *Docker) StreamExec(ctx context.Context, id string, cmd []string, stdin io.Reader, w io.Writer) (int, error) {
	e, err := d.c.CreateExec(dockerclient.CreateExecOptions{
		Context: ctx, Container: id, Cmd: cmd, AttachStdin: stdin != nil,
		AttachStdout: true, AttachStderr: true,
	})
	if err != nil {
		return 0, fmt.Errorf("create exec in %s: %w", id, err)
	}
	var stderr bytes.Buffer
	if err := d.c.StartExec(e.ID, dockerclient.StartExecOptions{
		Context: ctx, InputStream: stdin, OutputStream: w, ErrorStream: &stderr,
	}); err != nil {
		return 0, fmt.Errorf("start exec in %s: %w", id, err)
	}
	ins, err := d.c.InspectExec(e.ID)
	if err != nil {
		return 0, fmt.Errorf("inspect exec in %s: %w", id, err)
	}
	if ins.ExitCode != 0 {
		return ins.ExitCode, fmt.Errorf("exec in %s: exit %d: %s", id, ins.ExitCode, stderr.String())
	}
	return ins.ExitCode, nil
}

// Upload extracts a tar stream into dir inside the container.
func (d *Docker) Upload(ctx context.Context, id, dir string, r io.Reader) error {
	if err := d.c.UploadToContainer(id, dockerclient.UploadToContainerOptions{
		Context: ctx, Path: dir, InputStream: r,
	}); err != nil {
		return fmt.Errorf("upload to %s in %s: %w", dir, id, err)
	}
	return nil
}
