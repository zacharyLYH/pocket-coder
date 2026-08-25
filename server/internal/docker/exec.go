package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"

	dockerclient "github.com/fsouza/go-dockerclient"
)

// ExecResult is the outcome of a run-to-completion exec.
type ExecResult struct {
	ExitCode int
	Output   string // stdout (+ stderr when tty) combined
}

// The engine's exec start request embeds the streams in its JSON body, and
// encoding/json rejects func-typed values — so caller-supplied readers and
// writers are wrapped in boring structs that marshal to {}.
type streamReader struct{ r io.Reader }

func (s streamReader) Read(p []byte) (int, error) { return s.r.Read(p) }

type streamWriter struct{ w io.Writer }

func (s streamWriter) Write(p []byte) (int, error) { return s.w.Write(p) }

// Exec runs cmd in the container to completion and captures its output.
func (d *Docker) Exec(ctx context.Context, id string, cmd []string, tty bool) (ExecResult, error) {
	e, err := d.c.CreateExec(dockerclient.CreateExecOptions{
		Context: ctx, Container: id, Cmd: cmd, AttachStdout: true, AttachStderr: true, Tty: tty,
	})
	if err != nil {
		return ExecResult{}, fmt.Errorf("create exec in %s: %w", id, err)
	}
	var buf bytes.Buffer
	if err := d.c.StartExec(e.ID, dockerclient.StartExecOptions{
		Context: ctx, OutputStream: &buf, ErrorStream: &buf, Tty: tty, RawTerminal: tty,
	}); err != nil {
		return ExecResult{}, fmt.Errorf("start exec in %s: %w", id, err)
	}
	ins, err := d.c.InspectExec(e.ID)
	if err != nil {
		return ExecResult{}, fmt.Errorf("inspect exec in %s: %w", id, err)
	}
	return ExecResult{ExitCode: ins.ExitCode, Output: buf.String()}, nil
}

// ExecDone is the outcome of a finished interactive attach, delivered on
// its channel when the command exits or fails.
type ExecDone struct {
	ExitCode int
	Err      error
}

// Attach hijacks an interactive exec (a TTY when tty is set) and returns
// immediately with the exec id — callers need it for ResizeTTY while the
// command is still running. The exit code arrives on done once the command
// finishes; Err carries a stream/engine failure instead of an exit code.
func (d *Docker) Attach(ctx context.Context, id string, cmd []string, stdin io.Reader, stdout, stderr io.Writer, tty bool) (string, <-chan ExecDone, error) {
	if stdin != nil {
		stdin = streamReader{r: stdin}
	}
	stdout, stderr = streamWriter{w: stdout}, streamWriter{w: stderr}
	e, err := d.c.CreateExec(dockerclient.CreateExecOptions{
		Context: ctx, Container: id, Cmd: cmd,
		AttachStdin: stdin != nil, AttachStdout: true, AttachStderr: true, Tty: tty,
	})
	if err != nil {
		return "", nil, fmt.Errorf("create exec in %s: %w", id, err)
	}
	done := make(chan ExecDone, 1)
	go func() {
		serr := d.c.StartExec(e.ID, dockerclient.StartExecOptions{
			Context: ctx, InputStream: stdin, OutputStream: stdout, ErrorStream: stderr, Tty: tty, RawTerminal: tty,
		})
		if serr != nil {
			done <- ExecDone{Err: fmt.Errorf("attach exec %s: %w", e.ID, serr)}
			return
		}
		ins, ierr := d.c.InspectExec(e.ID)
		if ierr != nil {
			done <- ExecDone{Err: fmt.Errorf("inspect exec %s: %w", e.ID, ierr)}
			return
		}
		done <- ExecDone{ExitCode: ins.ExitCode}
	}()
	return e.ID, done, nil
}

// ResizeTTY resizes the TTY of a running interactive exec (the web terminal).
func (d *Docker) ResizeTTY(ctx context.Context, execID string, height, width int) error {
	if err := d.c.ResizeExecTTY(execID, height, width); err != nil {
		return fmt.Errorf("resize exec %s: %w", execID, err)
	}
	return nil
}
