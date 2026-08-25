package textutil

import (
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	long := strings.Repeat("x", 400)
	if got := Tail(long); len(got) != 300 {
		t.Fatalf("tail length = %d, want 300", len(got))
	}
	multi := "a\nb\nc\nd\ne\nf\ng"
	if got := Tail(multi); !strings.Contains(got, "g") || strings.Contains(got, "a\n") {
		t.Fatalf("tail should keep the last lines: %q", got)
	}
}
