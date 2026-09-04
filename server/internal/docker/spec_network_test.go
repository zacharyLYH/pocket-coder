package docker

import "testing"

func TestNetworkNamespace(t *testing.T) {
	if got := NetworkNamespace("project-container"); got != "container:project-container" {
		t.Fatalf("got %q", got)
	}
	if got := NetworkNamespace(""); got != "" {
		t.Fatalf("empty id got %q", got)
	}
}
