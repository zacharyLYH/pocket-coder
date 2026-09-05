package httpapi

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// Liveness guards for the terminal bridge: whatever the pty or the browser
// does, runTerminal must unwind — the deferred terminal.detach event is the
// observable proof. A wedged handler leaks its goroutine, the hijacked
// Docker connection, and one FD per abused tab, forever.

// waitForDetach polls the event log until terminal.detach lands, failing
// with a timeout if the handler never unwinds.
func waitForDetach(t *testing.T, d Deps) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if hasEventType(d, "terminal.detach") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("terminal.detach never landed: handler did not unwind")
}

// The tmux session ends while an input frame is still in flight: after the
// exec stream closes, nothing on the Docker side reads stdin anymore, so a
// bridge that couples ws framing to pipe writes parks its only reader inside
// the write and never notices the session ending.
func TestTerminalUnwindsWhenSessionEndsWithInputInFlight(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Attach(mock.Anything, "pcoder-abc", mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, true).
		RunAndReturn(func(ctx context.Context, _ string, _ []string, stdin io.Reader, _, _ io.Writer, _ bool) (string, <-chan docker.ExecDone, error) {
			done := make(chan docker.ExecDone, 1)
			go func() {
				b := make([]byte, 1)
				_, _ = stdin.Read(b)               // consume one frame like the hijacked stream would
				time.Sleep(200 * time.Millisecond) // then the exec ends; the stream stops draining
				done <- docker.ExecDone{ExitCode: 0}
			}()
			return "exec-a", done, nil
		})

	h := New(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, wsURL(srv, "abc", "main"), loginCookie(t, h, pinOut))
	sendRaw(t, conn, `{"type":"input","data":"a"}`)
	time.Sleep(100 * time.Millisecond)              // "a" consumed; the pty now stalls before exiting
	sendRaw(t, conn, `{"type":"input","data":"b"}`) // in flight when the session ends

	waitForDetach(t, d)
}

// The pty stops consuming stdin entirely while the browser sends a keystroke
// and then vanishes (tab close). The bridge must unwind on the client going
// away instead of waiting for a session that will never end.
func TestTerminalUnwindsWhenClientVanishesOnStalledPty(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Attach(mock.Anything, "pcoder-abc", mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, true).
		RunAndReturn(func(ctx context.Context, _ string, _ []string, stdin io.Reader, _, _ io.Writer, _ bool) (string, <-chan docker.ExecDone, error) {
			done := make(chan docker.ExecDone, 1)
			go func() {
				b := make([]byte, 1)
				_, _ = stdin.Read(b) // consume exactly one frame, then stall forever
				<-ctx.Done()
				done <- docker.ExecDone{ExitCode: 0}
			}()
			return "exec-b", done, nil
		})

	h := New(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, wsURL(srv, "abc", "main"), loginCookie(t, h, pinOut))
	sendRaw(t, conn, `{"type":"input","data":"a"}`) // consumed
	time.Sleep(100 * time.Millisecond)
	sendRaw(t, conn, `{"type":"input","data":"b"}`) // nobody reads this one
	time.Sleep(100 * time.Millisecond)
	_ = conn.Close() // the tab went away

	waitForDetach(t, d)
}
