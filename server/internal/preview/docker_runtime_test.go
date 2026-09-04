package preview

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sps/internal/docker"
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
	if err := WaitForCDP(ctx, client, "http://private:9222", time.Millisecond); err != nil {
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
	if err := WaitForCDP(ctx, client, "http://private:9222", time.Hour); err == nil {
		t.Fatal("expected cancellation")
	}
}

type runtimeDockerFake struct {
	specs        []docker.Spec
	inspectCalls int
}

func (f *runtimeDockerFake) Run(_ context.Context, spec docker.Spec) (string, error) {
	f.specs = append(f.specs, spec)
	return "browser-cid", nil
}
func (f *runtimeDockerFake) Inspect(context.Context, string) (docker.Container, error) {
	f.inspectCalls++
	if f.inspectCalls == 1 {
		return docker.Container{Running: true, Status: "running"}, nil
	}
	return docker.Container{Running: true, Status: "running", NetworkIP: "10.0.0.8"}, nil
}
func (f *runtimeDockerFake) Stop(context.Context, string, time.Duration) error { return nil }
func (f *runtimeDockerFake) Remove(context.Context, string, bool) error        { return nil }

func TestDockerFactoryUsesPrivateProjectNamespace(t *testing.T) {
	fake := &runtimeDockerFake{}
	client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(r.URL.Path, "/json/version") {
			return nil, context.Canceled
		}
		return &http.Response{StatusCode: http.StatusOK,
			Body:   io.NopCloser(strings.NewReader(`{"webSocketDebuggerUrl":"ws://10.0.0.8:9222/devtools/browser/1"}`)),
			Header: make(http.Header), Request: r}, nil
	})}
	factory := &DockerFactory{Docker: fake, HTTPClient: client, PollEvery: time.Millisecond}
	w, err := factory.Start(context.Background(), Config{ProjectID: "p1", ContainerID: "sps-p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.specs) != 1 || fake.specs[0].Network != "container:sps-p1" || fake.specs[0].Name != "sps-preview-p1" || fake.inspectCalls != 2 {
		t.Fatalf("fake sidecar spec = %#v", fake.specs)
	}
	if got := w.Endpoint(); got.CDP != "http://10.0.0.8:9223" || got.Display != "http://10.0.0.8:6080" {
		t.Fatalf("endpoint = %#v", got)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
