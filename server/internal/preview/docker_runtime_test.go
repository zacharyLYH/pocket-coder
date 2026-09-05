package preview

import (
	"context"
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
	w, err := factory.Start(context.Background(), Config{ProjectID: "p1", ContainerID: "pcoder-p1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.specs) != 1 || fake.specs[0].Network != "container:pcoder-p1" || fake.specs[0].Name != "pcoder-preview-p1" || fake.inspectCalls != 2 {
		t.Fatalf("fake sidecar spec = %#v", fake.specs)
	}
	if got := w.Endpoint(); got.CDP != "http://10.0.0.8:9223" || got.Display != "http://10.0.0.8:6080" {
		t.Fatalf("endpoint = %#v", got)
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
