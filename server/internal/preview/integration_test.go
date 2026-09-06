//go:build integration

package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"pcoder/internal/docker"
)

// TestDockerFactoryStartsReachableWorker drives the real factory against the
// real engine: start one sidecar for a project container (default dev seed
// project "deadbeef", override with PCODER_ITEST_PROJECT), then verify the
// returned loopback endpoint answers CDP from this process — the exact
// contract the HTTP handlers depend on, on every engine (including Docker
// Desktop, where container bridge IPs are not routable from the host).
func TestDockerFactoryStartsReachableWorker(t *testing.T) {
	projectID := os.Getenv("PCODER_ITEST_PROJECT")
	if projectID == "" {
		projectID = "deadbeef"
	}
	container := "pcoder-" + projectID

	d, err := docker.New(os.Getenv("PCODER_DOCKER_SOCK"))
	if err != nil {
		t.Fatalf("new docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := d.Ping(ctx); err != nil {
		t.Fatalf("docker unavailable: %v", err)
	}
	if _, err := d.Inspect(ctx, container); err != nil {
		t.Skipf("project container %s not present — is the dev stack up? (%v)", container, err)
	}

	// Fresh engine-backed factory: real sidecar, real probe/relay decision.
	factory := &DockerFactory{Docker: d}
	startCtx, startCancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer startCancel()
	w, err := factory.Start(startCtx, Config{ProjectID: projectID, ContainerID: container, Port: 3000})
	if err != nil {
		t.Fatalf("start worker: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		if err := w.Close(closeCtx); err != nil {
			t.Logf("worker close: %v", err)
		}
	})

	// The worker's endpoint must answer CDP from THIS process (the host) —
	// that is the whole contract the HTTP handlers depend on.
	ep := w.Endpoint()
	client := &http.Client{Timeout: 3 * time.Second}
	var lastErr error
	for attempt := 0; attempt < 40; attempt++ {
		res, err := client.Get(ep.CDP + "/json/version")
		if err == nil {
			var body struct {
				WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(res.Body).Decode(&body)
			res.Body.Close()
			if res.StatusCode == http.StatusOK && decodeErr == nil && body.WebSocketDebuggerURL != "" {
				t.Logf("CDP reachable at %s (browser %s)", ep.CDP, body.WebSocketDebuggerURL)
				return
			}
			lastErr = fmt.Errorf("status %d decode %v", res.StatusCode, decodeErr)
		} else {
			lastErr = err
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("worker endpoint %s never answered CDP from the host: %v", ep.CDP, lastErr)
}
