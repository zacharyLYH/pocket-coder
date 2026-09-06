package preview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pcoder/internal/docker"
)

func TestWaitForCDPRequiresDebuggerWebSocket(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if calls.Add(1) < 3 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: http.NoBody, Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(`{"webSocketDebuggerUrl":"ws://private/devtools/browser/1"}`)), Request: r}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := WaitForCDP(ctx, client, "http://private:9223", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d, want 3", calls.Load())
	}
}

func TestWaitForCDPHonorsCancellation(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := WaitForCDP(ctx, client, "http://private:9223", time.Hour); err == nil {
		t.Fatal("expected cancellation")
	}
}

const (
	sidecarIP      = "10.0.0.8"
	directEndpoint = "http://" + sidecarIP + ":9223"
	relayCDP       = "http://127.0.0.1:49153"
	relayDisplay   = "http://127.0.0.1:49154"
)

type runtimeDockerFake struct {
	specs       []docker.Spec
	removed     []string
	inspect     docker.Container // returned for the sidecar
	relayPub    map[int]int      // returned for the relay (nil → no publications)
	runErr      error            // first Run fails with this (then nil)
	inspectSeen int
}

func (f *runtimeDockerFake) Run(_ context.Context, spec docker.Spec) (string, error) {
	if f.runErr != nil {
		err := f.runErr
		f.runErr = nil
		return "", err
	}
	f.specs = append(f.specs, spec)
	if strings.HasPrefix(spec.Name, relayNamePrefix) {
		return "relay-cid", nil
	}
	return "browser-cid", nil
}

func (f *runtimeDockerFake) Inspect(_ context.Context, id string) (docker.Container, error) {
	f.inspectSeen++
	if id == "relay-cid" {
		return docker.Container{Running: true, Status: "running", Published: f.relayPub}, nil
	}
	return f.inspect, nil
}

func (f *runtimeDockerFake) Stop(context.Context, string, time.Duration) error { return nil }

func (f *runtimeDockerFake) Remove(_ context.Context, id string, _ bool) error {
	f.removed = append(f.removed, id)
	return nil
}

// cdpClient answers /json/version with a valid debugger payload for every
// host in okHosts and refuses everything else (simulating a dial refused).
func cdpClient(t *testing.T, okHosts ...string) *http.Client {
	t.Helper()
	return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/json/version") {
			return nil, context.Canceled
		}
		for _, host := range okHosts {
			if r.URL.Host == host {
				return &http.Response{StatusCode: http.StatusOK,
					Body:   io.NopCloser(strings.NewReader(`{"webSocketDebuggerUrl":"ws://h/devtools/browser/1"}`)),
					Header: make(http.Header), Request: r}, nil
			}
		}
		return nil, errors.New("dial: connection refused")
	})}
}

func TestDockerFactoryUsesPrivateNamespaceAndDirectIP(t *testing.T) {
	fake := &runtimeDockerFake{inspect: docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP}}
	factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t, sidecarIP+":9223"), PollEvery: time.Millisecond}
	w, err := factory.Start(context.Background(), Config{ProjectID: "p1", ContainerID: "pcoder-p1"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(context.Background())

	if len(fake.specs) != 1 {
		t.Fatalf("specs=%d, want 1 (no relay on the direct path)", len(fake.specs))
	}
	spec := fake.specs[0]
	if spec.Network != "container:pcoder-p1" || spec.Name != "pcoder-preview-p1" {
		t.Fatalf("spec = %#v", spec)
	}
	// Netns-shared containers cannot publish ports — none may be requested.
	if len(spec.PublishLoopback) != 0 {
		t.Fatalf("PublishLoopback = %v, want none (Docker rejects it)", spec.PublishLoopback)
	}
	if got := w.Endpoint(); got.CDP != directEndpoint || got.Display != "http://"+sidecarIP+":6080" {
		t.Fatalf("endpoint = %#v, want direct sidecar IP", got)
	}
}

func TestDockerFactoryRelayOnDesktop(t *testing.T) {
	fake := &runtimeDockerFake{
		inspect:  docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP},
		relayPub: map[int]int{9223: 49153, 6080: 49154},
	}
	// The sidecar IP is unroutable from the host (Docker Desktop); only the
	// relay's loopback publication answers.
	client := cdpClient(t, "127.0.0.1:49153")
	factory := &DockerFactory{Docker: fake, HTTPClient: client, PollEvery: time.Millisecond}
	w, err := factory.Start(context.Background(), Config{ProjectID: "p2", ContainerID: "pcoder-p2"})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close(context.Background())

	if len(fake.specs) != 2 {
		t.Fatalf("specs=%d, want sidecar + relay", len(fake.specs))
	}
	relay := fake.specs[1]
	if relay.Name != relayNamePrefix+"p2" || relay.Network != docker.DefaultNetwork {
		t.Fatalf("relay spec = %#v", relay)
	}
	if len(relay.PublishLoopback) != 2 {
		t.Fatalf("relay publications = %v", relay.PublishLoopback)
	}
	// The relay forwards the socat proxy port, not Chromium's localhost-only CDP.
	if relay.Cmd[0] == "" || !strings.Contains(relay.Cmd[0], "TCP:"+sidecarIP+":9223") || strings.Contains(relay.Cmd[0], "TCP:"+sidecarIP+":9222") {
		t.Fatalf("relay cmd = %q", relay.Cmd[0])
	}
	if got := w.Endpoint(); got.CDP != relayCDP || got.Display != relayDisplay {
		t.Fatalf("endpoint = %#v, want loopback relay ports", got)
	}
	// Closing the worker must tear down BOTH containers.
	_ = w.Close(context.Background())
	if len(fake.removed) != 2 {
		t.Fatalf("removed = %v, want sidecar + relay", fake.removed)
	}
}

func TestDockerFactoryRelayWithoutPublicationsFails(t *testing.T) {
	fake := &runtimeDockerFake{inspect: docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP}}
	factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t) /* nothing answers */}
	if _, err := factory.Start(context.Background(), Config{ProjectID: "p3", ContainerID: "pcoder-p3"}); err == nil {
		t.Fatal("expected error for missing relay publications")
	}
	if len(fake.removed) != 2 {
		t.Fatalf("removed = %v, want sidecar + failed relay cleaned up", fake.removed)
	}
}

func TestDockerFactoryExitedSidecarFails(t *testing.T) {
	fake := &runtimeDockerFake{inspect: docker.Container{Running: false, Status: "exited"}}
	factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t)}
	if _, err := factory.Start(context.Background(), Config{ProjectID: "p4", ContainerID: "pcoder-p4"}); err == nil {
		t.Fatal("expected error when sidecar exits before CDP")
	}
	if len(fake.removed) != 1 || fake.removed[0] != "browser-cid" {
		t.Fatalf("removed = %v, want failed sidecar cleaned up", fake.removed)
	}
}

func TestDockerFactoryCDPTimeoutFailsAndCleansUp(t *testing.T) {
	fake := &runtimeDockerFake{inspect: docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP},
		relayPub: map[int]int{9223: 49153, 6080: 49154}}
	// The relay answers Inspect but CDP never answers through it: the wait
	// must time out (bounded) and clean up sidecar + relay.
	factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t), PollEvery: time.Millisecond, CDPWait: 20 * time.Millisecond}
	if _, err := factory.Start(context.Background(), Config{ProjectID: "p5", ContainerID: "pcoder-p5"}); err == nil {
		t.Fatal("expected CDP wait timeout error")
	}
	if len(fake.removed) != 2 {
		t.Fatalf("removed = %v, want sidecar + relay cleaned up", fake.removed)
	}
}

func TestDockerFactoryRetriesAfterStaleSidecar(t *testing.T) {
	fake := &runtimeDockerFake{
		inspect: docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP},
		runErr:  errors.New("container pcoder-preview-p6 already exists"),
	}
	factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t, sidecarIP+":9223"), PollEvery: time.Millisecond}
	if _, err := factory.Start(context.Background(), Config{ProjectID: "p6", ContainerID: "pcoder-p6"}); err != nil {
		t.Fatal(err)
	}
	// The failed first Run appends no spec (stale container removal is the
	// only side effect), so one spec = the successful retry.
	if len(fake.specs) != 1 || len(fake.removed) != 1 || fake.removed[0] != "pcoder-preview-p6" {
		t.Fatalf("specs=%d removed=%v, want one stale-container retry", len(fake.specs), fake.removed)
	}
}

func TestDockerFactoryBrowserTarget(t *testing.T) {
	cases := []struct {
		name       string
		port       int
		wantTarget string
	}{
		{"zero_defaults_to_3000", 0, "BROWSER_TARGET=http://127.0.0.1:3000"},
		{"explicit_3000", 3000, "BROWSER_TARGET=http://127.0.0.1:3000"},
		{"vite_default_5173", 5173, "BROWSER_TARGET=http://127.0.0.1:5173"},
		{"common_8080", 8080, "BROWSER_TARGET=http://127.0.0.1:8080"},
		{"min_port", 1, "BROWSER_TARGET=http://127.0.0.1:1"},
		{"max_port", 65535, "BROWSER_TARGET=http://127.0.0.1:65535"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &runtimeDockerFake{inspect: docker.Container{Running: true, Status: "running", NetworkIP: sidecarIP}}
			factory := &DockerFactory{Docker: fake, HTTPClient: cdpClient(t, sidecarIP+":9223"), PollEvery: time.Millisecond}
			_, err := factory.Start(context.Background(), Config{ProjectID: "bt", ContainerID: "pcoder-bt", Port: tc.port})
			if err != nil {
				t.Fatal(err)
			}
			if len(fake.specs) != 1 {
				t.Fatalf("specs=%d, want 1", len(fake.specs))
			}
			var found string
			for _, env := range fake.specs[0].Env {
				if strings.HasPrefix(env, "BROWSER_TARGET=") {
					found = env
					break
				}
			}
			if found != tc.wantTarget {
				t.Fatalf("BROWSER_TARGET=%q, want %q", found, tc.wantTarget)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
