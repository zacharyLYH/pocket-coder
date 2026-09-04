package docker

import "testing"

func TestContainerNetworkIPIsOptional(t *testing.T) {
	// Container is intentionally a small summary. An empty IP is valid while
	// Docker is restarting or when a mock does not model networking.
	if (Container{}).NetworkIP != "" {
		t.Fatal("zero container should have no network IP")
	}
}
