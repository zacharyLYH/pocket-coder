package preview

import (
	"context"
	"errors"
	"testing"
	"time"

	"pcoder/internal/docker"
)

type listerFake struct {
	containers []docker.ContainerInfo
	stopped    []string
	removed    []string
	stopErr    error
}

func (f *listerFake) ListContainers(context.Context, string) ([]docker.ContainerInfo, error) {
	return f.containers, nil
}

func (f *listerFake) Stop(_ context.Context, id string, _ time.Duration) error {
	f.stopped = append(f.stopped, id)
	return f.stopErr
}

func (f *listerFake) Remove(_ context.Context, id string, _ bool) error {
	f.removed = append(f.removed, id)
	return nil
}

// The sweep is ownership-based: containers named after a project with a live
// worker stay, everything else (sidecar and relay alike) goes. This is what
// reclaims the containers a killed server leaves behind.
func TestSweepContainersRemovesUnownedPreviews(t *testing.T) {
	m := NewManager(&fakeFactory{})
	// Only owner/live has a worker: the e2e/default-1 containers below are the
	// leftovers a killed server leaves behind.
	if _, err := m.Ensure(context.Background(), Config{ProjectID: "owner/live", ContainerID: "pcoder-owner-live"}); err != nil {
		t.Fatal(err)
	}
	f := &listerFake{containers: []docker.ContainerInfo{
		{ID: "live-sidecar", Name: "pcoder-preview-owner-live", Running: true},
		{ID: "live-relay", Name: "pcoder-preview-relay-owner-live", Running: true},
		{ID: "dead-sidecar", Name: "pcoder-preview-e2e-default-1", Running: true},
		{ID: "dead-relay", Name: "pcoder-preview-relay-e2e-default-1", Running: true},
	}}
	removed, err := m.SweepContainers(context.Background(), f)
	if err != nil {
		t.Fatalf("sweep = %v", err)
	}
	if len(removed) != 2 || removed[0] != "pcoder-preview-e2e-default-1" || removed[1] != "pcoder-preview-relay-e2e-default-1" {
		t.Fatalf("removed = %v, want the two unowned containers", removed)
	}
	if len(f.stopped) != 2 || len(f.removed) != 2 {
		t.Fatalf("stopped=%v removed=%v, want both acted on", f.stopped, f.removed)
	}
	for _, id := range f.removed {
		if id == "live-sidecar" || id == "live-relay" {
			t.Fatalf("sweep touched a live worker's container: %v", f.removed)
		}
	}
}

// A live worker's container is never swept, whatever state Docker reports:
// the manager is the ownership record, not the container's run state.
func TestSweepContainersKeepsLiveWorkerContainers(t *testing.T) {
	m := NewManager(&fakeFactory{})
	if _, err := m.Ensure(context.Background(), Config{ProjectID: "p1", ContainerID: "pcoder-p1"}); err != nil {
		t.Fatal(err)
	}
	f := &listerFake{containers: []docker.ContainerInfo{
		{ID: "sidecar", Name: "pcoder-preview-p1", Running: false},
	}}
	removed, err := m.SweepContainers(context.Background(), f)
	if err != nil || len(removed) != 0 {
		t.Fatalf("sweep = %v removed=%v, want a live worker's container left alone", err, removed)
	}
}

// A start in flight owns its container: the sidecar exists before Ensure
// returns, so a sweep landing mid-start must not delete the preview the user
// is opening.
func TestSweepContainersSparesAnInFlightStart(t *testing.T) {
	f := &fakeFactory{delay: 300 * time.Millisecond}
	m := NewManager(f)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := m.Ensure(context.Background(), Config{ProjectID: "starting/now", ContainerID: "pcoder-starting-now"}); err != nil {
			t.Errorf("ensure: %v", err)
		}
	}()
	// Wait for the start to register as in-flight, then sweep underneath it.
	for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
		m.mu.RLock()
		inflight := len(m.inflight) > 0
		m.mu.RUnlock()
		if inflight {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("start never became in-flight")
		}
	}
	lister := &listerFake{containers: []docker.ContainerInfo{
		{ID: "sidecar", Name: "pcoder-preview-starting-now", Running: true},
		{ID: "relay", Name: "pcoder-preview-relay-starting-now", Running: true},
	}}
	removed, err := m.SweepContainers(context.Background(), lister)
	if err != nil {
		t.Fatalf("sweep = %v", err)
	}
	<-done
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want an in-flight start's containers spared", removed)
	}
}

// A failure to stop one container must not stop the sweep or hide the cause:
// a boot-time sweep reports up to boot and the container stays for the next
// boot rather than failing the whole recovery pass.
func TestSweepContainersReportsFirstFailureAndContinues(t *testing.T) {
	m := NewManager(&fakeFactory{})
	f := &listerFake{
		containers: []docker.ContainerInfo{
			{ID: "a", Name: "pcoder-preview-one"},
			{ID: "b", Name: "pcoder-preview-two"},
		},
		stopErr: errors.New("engine hiccup"),
	}
	removed, err := m.SweepContainers(context.Background(), f)
	if err == nil {
		t.Fatal("expected the stop failure to surface")
	}
	if len(removed) != 2 || len(f.removed) != 2 {
		t.Fatalf("removed=%v removedIDs=%v, want the sweep to continue past a failure", removed, f.removed)
	}
}

func TestSweepContainersNoContainersIsANoOp(t *testing.T) {
	m := NewManager(&fakeFactory{})
	f := &listerFake{}
	removed, err := m.SweepContainers(context.Background(), f)
	if err != nil || len(removed) != 0 || len(f.stopped) != 0 {
		t.Fatalf("sweep = %v removed=%v stopped=%v, want no work", err, removed, f.stopped)
	}
}

// The same slug extraction must hold for both container families, or a sweep
// would delete a live worker's relay.
func TestPreviewSlugUnwrapsSidecarAndRelay(t *testing.T) {
	for name, want := range map[string]string{
		"pcoder-preview-owner-repo":       "owner-repo",
		"pcoder-preview-relay-owner-repo": "owner-repo",
		"pcoder-preview-e2e-default-1":    "e2e-default-1",
	} {
		if got := previewSlug(name); got != want {
			t.Fatalf("previewSlug(%q) = %q, want %q", name, got, want)
		}
	}
}
