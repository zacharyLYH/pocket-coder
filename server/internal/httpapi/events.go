// Event stream endpoint: paginated read of the append-only event log.
package httpapi

import (
	"net/http"
	"strconv"

	"sps/internal/events"
)

func handleEvents(ev events.Reader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		after := int64(0)
		if v := r.URL.Query().Get("after"); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeErr(w, http.StatusBadRequest, "after must be a number")
				return
			}
			after = n
		}
		limit := 100
		if v := r.URL.Query().Get("limit"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeErr(w, http.StatusBadRequest, "limit must be a non-negative number")
				return
			}
			limit = n
		}
		if limit > 1000 {
			limit = 1000
		}
		list, err := ev.Read(after, limit)
		if err != nil {
			writeInternalErr(w, "read events", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": list})
	}
}
