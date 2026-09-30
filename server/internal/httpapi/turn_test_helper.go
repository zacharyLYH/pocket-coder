package httpapi

import (
  "encoding/json"
  "net/http/httptest"
  "testing"
)

// finalBody parses a plain-JSON turn response into its final object.
// Replaces the old SSE splitSSEBody contract: no status lines exist.
func finalBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
  t.Helper()
  var last map[string]any
  if err := json.Unmarshal(rec.Body.Bytes(), &last); err != nil {
    t.Fatalf("turn body not JSON: %v %q", err, rec.Body.String())
  }
  return last
}
