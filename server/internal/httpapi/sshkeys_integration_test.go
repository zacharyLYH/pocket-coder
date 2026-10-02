//go:build integration

// Integration tests for what is left of the SSH surface against a live
// engine: container reconciliation. The per-user key registry and the
// http clone method are gone — the server deploy key covers every clone.
// Run with: go test -tags=integration -count=1 ./internal/httpapi/
package httpapi

import (
	"net/http"
	"testing"

	"pcoder/internal/project"
)

func TestReconcileMissingContainer(t *testing.T) {
	h, dkr, _, pinOut, _, _ := newLiveDeps(t)
	cookie := login(t, h, pinOut)

	// create a project
	id, _ := createTestProject(t, h, cookie, fixtureRepo(t).URL, "")
	waitForStatus(t, h, cookie, id, "running")

	// manually kill the container (simulate engine restart / docker rm)
	if err := dkr.Stop(t.Context(), project.ContainerName(id), 0); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := dkr.Remove(t.Context(), project.ContainerName(id), true); err != nil {
		t.Fatalf("remove: %v", err)
	}

	// verify the container is gone
	if _, err := dkr.Inspect(t.Context(), project.ContainerName(id)); err == nil {
		t.Fatal("container should be gone")
	}

	// listing sessions should trigger reconciliation and succeed
	code, body := doJSON(t, h, cookie, http.MethodGet, projectPath(id, "/sessions"), "")
	if code != http.StatusOK {
		t.Fatalf("list sessions after reconcile: %d %v", code, body)
	}

	// verify the container is back
	waitForStatus(t, h, cookie, id, "running")
}
