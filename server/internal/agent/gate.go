package agent

import (
	"encoding/json"
	"strings"
)

// GateVerdict parses a boolean classifier verdict from raw model output.
// It fails open: true unless the named key parses cleanly as false, so a
// malformed or missing reply never blocks the turn.
func GateVerdict(raw, key string) bool {
	s := strings.TrimSpace(raw)
	if i := strings.Index(s, "{"); i > 0 {
		if j := strings.LastIndex(s, "}"); j > i {
			s = s[i : j+1]
		}
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return true
	}
	b, ok := v[key].(bool)
	if !ok {
		return true
	}
	return b
}
