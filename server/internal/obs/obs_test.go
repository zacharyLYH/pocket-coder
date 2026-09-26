package obs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testStore configures staged logging over a fresh store and returns it.
// Tests log through the real path and read back the files.
func testStore(t *testing.T, events func(typ string, data map[string]any)) *Store {
	t.Helper()
	s := NewStore(t.TempDir())
	t.Cleanup(s.Close)
	Configure(s, events)
	t.Cleanup(func() { Configure(nil, nil) })
	return s
}

func TestEnvelopeSingleLine(t *testing.T) {
	s := testStore(t, nil)
	ctx := WithTrace(context.Background(), "9f3a2c1d4e5f6a7b")
	ctx = WithProject(ctx, "owner/repo")
	Info(ctx, ProjectReady, "clone landed", map[string]any{"sha": "abc123"})
	p := s.path("owner/repo")
	// Buffered: nothing on disk until the flush, but the call returns now.
	if b, _ := os.ReadFile(p); len(b) != 0 {
		t.Fatalf("want empty file before flush, got %d bytes", len(b))
	}
	s.flush()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 1 {
		t.Fatalf("want 1 line, got %d", len(lines))
	}
	var back Entry
	if err := json.Unmarshal([]byte(lines[0]), &back); err != nil {
		t.Fatalf("not single-line JSON: %v", err)
	}
	if back.Seq != 1 || back.Type != "project.ready" || back.Trace != "9f3a2c1d4e5f6a7b" {
		t.Fatalf("decoded = %+v", back)
	}
}

// TestDropCount proves overflow drops and counts without blocking: a store
// with no running loop and a 1-slot chan loses the 2nd and 3rd of 3 appends.
func TestDropCount(t *testing.T) {
	s := &Store{dir: t.TempDir(), seq: map[string]*atomic.Int64{}, ch: make(chan item, 1), now: time.Now}
	for _, typ := range []string{"a", "b", "c"} {
		s.append("o/r", "none", "server", "info", typ, typ, time.Now(), nil)
	}
	if got := s.Dropped(); got != 2 {
		t.Fatalf("dropped = %d, want 2", got)
	}
}

// TestFanOut proves the triple write: file + events sink, with id/trace
// merged into the event data.
func TestFanOut(t *testing.T) {
	var got []map[string]any
	s := testStore(t, func(typ string, data map[string]any) {
		got = append(got, map[string]any{"type": typ, "data": data})
	})
	ctx := WithTrace(context.Background(), "9f3a2c1d4e5f6a7b")
	ctx = WithProject(ctx, "owner/repo")
	Error(ctx, "clone.failed", "clone failed sha abc1234", map[string]any{"stage": "x"})
	logs, _, _ := s.Read("owner/repo", 0, 0, 200, "", "", "", "", "")
	if len(logs) != 1 || logs[0].Level != "error" || logs[0].Trace != "9f3a2c1d4e5f6a7b" {
		t.Fatalf("file = %+v", logs)
	}
	if len(got) != 1 || got[0]["type"] != "clone.failed" {
		t.Fatalf("events = %+v", got)
	}
	data := got[0]["data"].(map[string]any)
	if data["id"] != "owner/repo" || data["trace"] != "9f3a2c1d4e5f6a7b" || data["stage"] != "x" {
		t.Fatalf("event data = %+v", data)
	}
}

// TestLevels proves each staged function maps to its file level, and error
// values stringify into attrs instead of marshaling as {}.
func TestLevels(t *testing.T) {
	s := testStore(t, nil)
	ctx := WithProject(context.Background(), "o/r")
	Info(ctx, "a", "a", nil)
	Warn(ctx, "b", "b", nil)
	Error(ctx, "c", "c", map[string]any{"err": errors.New("boom")})
	for _, want := range []struct {
		typ, level, err string
	}{
		{"a", "info", ""},
		{"b", "warn", ""},
		{"c", "error", "boom"},
	} {
		logs, _, _ := s.Read("o/r", 0, 0, 200, "", "", want.typ, "", "")
		if len(logs) != 1 || logs[0].Level != want.level {
			t.Fatalf("%s = %+v, want level %s", want.typ, logs, want.level)
		}
		if want.err != "" && logs[0].Attrs["err"] != "boom" {
			t.Fatalf("err attr = %+v", logs[0].Attrs)
		}
	}
}

// TestEmptyProject proves a missing project id is a silent no-op, never a
// half-written line.
func TestEmptyProject(t *testing.T) {
	s := testStore(t, func(typ string, data map[string]any) {
		t.Fatalf("events sink called for empty project: %s", typ)
	})
	Info(context.Background(), "x", "x", nil)
	if got := s.Dropped(); got != 0 {
		t.Fatalf("dropped = %d, want 0", got)
	}
}

func TestTemplate(t *testing.T) {
	a := Template("clone failed sha abc1234def attempt 3 from 10.0.0.5")
	b := Template("clone failed sha deadbeef99 attempt 12 from 192.168.1.1")
	if a != b {
		t.Fatalf("want grouped, got %q vs %q", a, b)
	}
	if !strings.Contains(a, "<hex>") || !strings.Contains(a, "<n>") || !strings.Contains(a, "<ip>") {
		t.Fatalf("not stripped: %q", a)
	}
}

func TestRotation(t *testing.T) {
	s := testStore(t, nil)
	ctx := WithProject(context.Background(), "o/r")
	big := strings.Repeat("x", 100<<10)
	for i := 0; i < 50; i++ {
		Info(ctx, "t", big, nil)
	}
	s.flush()
	if _, err := os.Stat(s.path("o/r") + ".1"); err != nil {
		t.Fatalf("want .1 backup after >4MB, err=%v", err)
	}
	logs, _, _ := s.Read("o/r", 0, 0, 1000, "", "", "", "", "")
	if len(logs) == 0 {
		t.Fatal("want readable logs after rotation")
	}
}

func TestReadCursors(t *testing.T) {
	s := testStore(t, nil)
	ctx := WithProject(context.Background(), "o/r")
	for _, typ := range []string{"a", "b", "c", "d", "e"} {
		Info(ctx, typ, typ, nil)
	}
	logs, first, last := s.Read("o/r", 2, 0, 200, "", "", "", "", "")
	if len(logs) != 3 || first != 3 || last != 5 {
		t.Fatalf("after=2 -> %+v first=%d last=%d", logs, first, last)
	}
	older, f, _ := s.Read("o/r", 0, 4, 200, "", "", "", "", "")
	if len(older) != 3 || f != 1 {
		t.Fatalf("before=4 -> %+v first=%d", older, f)
	}
	filt, _, _ := s.Read("o/r", 0, 0, 200, "", "", "c", "", "C")
	if len(filt) != 1 || filt[0].Type != "c" {
		t.Fatalf("filter -> %+v", filt)
	}
	multi, _, _ := s.Read("o/r", 0, 0, 200, "", "", "b, d", "", "")
	if len(multi) != 2 || multi[0].Type != "b" || multi[1].Type != "d" {
		t.Fatalf("multi-type -> %+v", multi)
	}
}

func TestSourceFacet(t *testing.T) {
	s := testStore(t, nil)
	ctx := WithProject(context.Background(), "o/s")
	Info(ctx, PreviewStart, "plain", nil)
	Info(WithSource(ctx, SourcePreview), PreviewStart, "sourced", nil)
	logs, _, _ := s.Read("o/s", 0, 0, 200, "", "", "", "", "")
	if len(logs) != 2 || logs[0].Source != SourceServer || logs[1].Source != SourcePreview {
		t.Fatalf("sources = %+v", logs)
	}
	filt, _, _ := s.Read("o/s", 0, 0, 200, "", SourcePreview, "", "", "")
	if len(filt) != 1 || filt[0].Msg != "sourced" {
		t.Fatalf("source filter -> %+v", filt)
	}
}

func TestTraceMiddleware(t *testing.T) {
	var got string
	h := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = TraceOf(r.Context())
	}))
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(got) != 16 {
		t.Fatalf("trace = %q, want hex16", got)
	}
	got = ""
	req.Header.Set("X-Trace", "abcdef0123456789")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got != "abcdef0123456789" {
		t.Fatalf("passthrough = %q", got)
	}
}
