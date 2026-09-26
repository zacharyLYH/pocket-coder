package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The shared framing contract: N status lines in order, then exactly one
// bare-JSON final. Every chatbot reuses it, so it is pinned once here.
func TestSSEFraming(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEHeaders(rec)
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content-type = %q", ct)
	}
	writeSSEStatus(rec, "model", "running")
	writeSSEStatus(rec, "done", "answered")
	writeSSEFinal(rec, map[string]any{"threadId": "t", "answer": "hi"})
	statuses, final := splitSSEBody(t, rec.Body.String())
	if len(statuses) != 2 || statuses[0]["tool"] != "model" || statuses[1]["tool"] != "done" {
		t.Fatalf("statuses = %v", statuses)
	}
	if final["threadId"] != "t" || final["answer"] != "hi" {
		t.Fatalf("final = %v", final)
	}
}

// A tool or status containing JSON-special bytes must be escaped inside the
// data line, never emitted raw: a \n in a tool name would forge a second
// SSE line (line injection), breaking the one-line-per-status contract.
func TestSSEStatusEscapesSpecials(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEHeaders(rec)
	writeSSEStatus(rec, "bad\ntool\"x", "run\\ning")
	writeSSEFinal(rec, map[string]any{})
	statuses, _ := splitSSEBody(t, rec.Body.String())
	if len(statuses) != 1 || statuses[0]["tool"] != "bad\ntool\"x" || statuses[0]["status"] != "run\\ning" {
		t.Fatalf("statuses = %v, want escaped specials round-tripped", statuses)
	}
}

// Status lines flush immediately: the sheet renders progress while the
// loop still runs, so an unflushed line is a silent regression.
func TestSSEStatusFlushes(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEHeaders(rec)
	writeSSEStatus(rec, "model", "running")
	if !rec.Flushed {
		t.Fatal("status line did not flush")
	}
}

// Final lines carry unicode (emoji, CJK) intact through the bare-JSON line.
func TestSSEFinalUnicode(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEHeaders(rec)
	writeSSEFinal(rec, map[string]any{"threadId": "t", "answer": "准备好了 ✓ 🚀"})
	_, final := splitSSEBody(t, rec.Body.String())
	if final["answer"] != "准备好了 ✓ 🚀" {
		t.Fatalf("final = %v, want unicode intact", final)
	}
}

// CRLF framing from proxies or other SSE implementations must still parse.
func TestSSESplitCRLF(t *testing.T) {
	rec := httptest.NewRecorder()
	writeSSEHeaders(rec)
	writeSSEStatus(rec, "model", "running")
	writeSSEFinal(rec, map[string]any{"threadId": "t", "answer": "hi"})
	statuses, final := splitSSEBody(t, strings.ReplaceAll(rec.Body.String(), "\n", "\r\n"))
	if len(statuses) != 1 || final["answer"] != "hi" {
		t.Fatalf("crlf: statuses=%v final=%v", statuses, final)
	}
}
