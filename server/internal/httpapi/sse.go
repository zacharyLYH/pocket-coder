// Shared SSE turn-stream framing: status lines are "data: {...}\n\n",
// the final answer is one bare-JSON line (no data: prefix) so the client
// tells status vs result apart by prefix. Codemap stays plain JSON; this
// is the future path any chatbot reuses.
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
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
