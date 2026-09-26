// Shared SSE turn-stream framing: status lines are "data: {...}\n\n",
// the final answer is one bare-JSON line (no data: prefix) so the client
// tells status vs result apart by prefix. Codemap stays plain JSON; this
// is the future path any chatbot reuses.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// writeSSEHeaders starts a 200 turn stream. Validation errors must be
// answered before this (as JSON), since headers can only be written once.
func writeSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
}

// writeSSEStatus emits one status line and flushes so the client renders
// progress while the loop still runs.
func writeSSEStatus(w http.ResponseWriter, tool, status string) {
	raw, _ := json.Marshal(map[string]any{"tool": tool, "status": status})
	fmt.Fprintf(w, "data: %s\n\n", raw)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// writeSSEFinal appends the bare-JSON answer line ending the stream.
func writeSSEFinal(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v)
	_, _ = w.Write(append(raw, '\n'))
}

// splitSSEBody is the test contract: every "data:" line parses as a status,
// exactly one trailing bare-JSON line parses as the final. Used by handler
// tests to pin order, not just substring presence.
func splitSSEBody(t interface {
	Helper()
	Fatalf(string, ...any)
}, body string) (statuses []map[string]any, final map[string]any) {
	t.Helper()
	var finals []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "data:") {
			var s map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))), &s); err != nil {
				t.Fatalf("bad status line %q: %v", line, err)
			}
			statuses = append(statuses, s)
			continue
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
			t.Fatalf("bad final line %q: %v", line, err)
		}
		finals = append(finals, trimmed)
		// Keep the last parseable object; error bodies also parse.
		final = v
	}
	if len(finals) != 1 {
		t.Fatalf("want exactly 1 final line, got %d in %q", len(finals), body)
	}
	return statuses, final
}
