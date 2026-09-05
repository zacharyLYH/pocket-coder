package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/mock"

	"pcoder/internal/docker"
)

// The pty hands us raw bytes in arbitrary chunks; a multi-byte character can
// straddle two reads. Whatever we put in a JSON text frame must be valid
// UTF-8, so the bridge must hold back a trailing partial character and let
// the next chunk complete it. This test writes "héllo" as two chunks that
// split the é and expects the browser to see it intact.
func TestTerminalOutputSurvivesUTF8SplitAcrossFrames(t *testing.T) {
	d, md, pinOut, dataDir := newSessionDeps(t)
	seedProject(t, dataDir, "abc")

	md.EXPECT().Inspect(mock.Anything, "pcoder-abc").Return(docker.Container{Running: true}, nil)
	md.EXPECT().Exec(mock.Anything, "pcoder-abc", []string{"tmux", "has-session", "-t", "main"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	md.EXPECT().Attach(mock.Anything, "pcoder-abc", mock.Anything,
		mock.Anything, mock.Anything, mock.Anything, true).
		RunAndReturn(func(ctx context.Context, _ string, _ []string, stdin io.Reader, stdout, _ io.Writer, _ bool) (string, <-chan docker.ExecDone, error) {
			done := make(chan docker.ExecDone, 1)
			go func() {
				// é is 0xC3 0xA9; cut right between the two bytes.
				_, _ = stdout.Write([]byte("h\xc3"))
				time.Sleep(100 * time.Millisecond) // force two separate frames
				_, _ = stdout.Write([]byte("\xa9llo"))
				done <- docker.ExecDone{ExitCode: 0}
			}()
			return "exec-u", done, nil
		})

	h := New(d)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, wsURL(srv, "abc", "main"), loginCookie(t, h, pinOut))
	defer conn.Close()

	var got []byte
	for {
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read frames: %v", err)
		}
		var f struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Code int    `json:"code"`
		}
		if err := json.Unmarshal(raw, &f); err != nil {
			t.Fatalf("bad frame %q: %v", raw, err)
		}
		if f.Type == "output" {
			got = append(got, f.Data...)
		}
		if f.Type == "exit" {
			break
		}
	}
	if string(got) != "héllo" {
		t.Fatalf("browser saw %q (% x), want \"héllo\"", got, got)
	}
}

var _ = websocket.TextMessage // keep the import if the loop above changes

// Table test for the boundary finder itself: emoji (4 bytes), CJK (3 bytes),
// ASCII passthrough, and malformed bytes that must never be held forever.
func TestSplitUTF8(t *testing.T) {
	cases := []struct {
		name string
		in   string
		n    int // frameable prefix length
		hold int // trailing bytes to carry
	}{
		{"pure ascii", "hello", 5, 0},
		{"complete 2-byte", "h\xc3\xa9", 3, 0},
		{"split 2-byte", "h\xc3", 1, 1},
		{"complete 3-byte", "\xe4\xb8\xad", 3, 0},
		{"split 3-byte start only", "a\xe4", 1, 1},
		{"split 3-byte two in", "a\xe4\xb8", 1, 2},
		{"complete 4-byte emoji", "ok\xf0\x9f\x98\x80", 6, 0},
		{"split 4-byte three in", "ok\xf0\x9f\x98", 2, 3},
		{"orphan continuations pass through", "ab\x80\x80\x80", 5, 0},
		{"invalid start passes through", "ab\xff", 3, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, hold := splitUTF8([]byte(tc.in))
			if n != tc.n || hold != tc.hold {
				t.Fatalf("splitUTF8(%q) = (%d, %d), want (%d, %d)", tc.in, n, hold, tc.n, tc.hold)
			}
			if n+hold != len(tc.in) {
				t.Fatalf("n+hold = %d, input is %d bytes: bytes lost or invented", n+hold, len(tc.in))
			}
		})
	}
}
