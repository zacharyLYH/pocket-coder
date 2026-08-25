// Package textutil holds small shared string helpers.
package textutil

import "strings"

// Tail keeps the last bit of long command output for an error message:
// at most 5 lines joined with " | ", capped to the final 300 characters.
func Tail(out string) string {
	out = strings.TrimSpace(out)
	lines := strings.Split(out, "\n")
	if len(lines) > 5 {
		lines = lines[len(lines)-5:]
	}
	t := strings.Join(lines, " | ")
	if len(t) > 300 {
		t = t[len(t)-300:]
	}
	return t
}
