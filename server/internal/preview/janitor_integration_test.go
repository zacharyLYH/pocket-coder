//go:build integration

package preview

import (
	"context"
	"testing"

	"pcoder/internal/docker"
)

// The boot-time sweep's contract against a real engine: orphaned preview
// sidecars and relays are stopped and removed, a live worker's container
// survives, and a second pass is a no-op. Unit tests cover the ownership
// logic; this proves the listing + removal calls actually work on Docker.
func TestJanitorSweepsOrphanedPreviewContainers(t *testing.T) {
	dkr, err := docker.New("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := dkr.Ping(ctx); err != nil {
		t.Skipf("docker unavailable — is the engine running? %v", err)
	}
	orphans := []string{"pcoder-preview-janitor-check", "pcoder-preview-relay-janitor-check", "pcoder-preview-janitor-live"}
	for _, name := range orphans {
		id, err := dkr.Run(ctx, docker.Spec{Name: name, Image: "alpine:latest", Cmd: []string{"sleep", "120"}})
		if err != nil {
			t.Fatalf("run %s: %v", name, err)
		}
		t.Cleanup(func() { _ = dkr.Remove(context.Background(), id, true) })
	}

	// janitor/live is owned by a worker; janitor/check is not.
	m := NewManager(&fakeFactory{})
	if _, err := m.Ensure(ctx, Config{ProjectID: "janitor/live", ContainerID: "pcoder-janitor-live"}); err != nil {
		t.Fatal(err)
	}
	removed, err := m.SweepContainers(ctx, dkr)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(removed) != 2 {
		t.Fatalf("removed = %v, want the unowned project's sidecar + relay", removed)
	}
	left, err := dkr.ListContainers(ctx, sidecarNamePrefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name != "pcoder-preview-janitor-live" {
		t.Fatalf("after sweep: %+v, want only the live worker's container", left)
	}
	if again, err := m.SweepContainers(ctx, dkr); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %v (err %v), want a no-op", again, err)
	}
}
