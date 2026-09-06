package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pcoder/internal/docker"
)

const (
	DefaultBrowserImage = "pcoder-browser:v12"
	// SidecarPorts are the preview browser's own ports. The sidecar shares
	// the project's network namespace, so ss lists them alongside user
	// ports. Keep this as the single source of truth — preview port
	// filtering must derive from here, not duplicate literals.
	SidecarVNCPort      = 5900 // x11vnc -rfbport (see image/start-browser)
	SidecarChromiumPort = 9222 // Chromium --remote-debugging-port (CDP_PORT)
	SidecarCDPProxyPort = 9223 // socat forward (CDP_PROXY_PORT)
	SidecarNoVNCPort    = 6080 // websockify (VNC_WEB_PORT)

	// CDP goes through the socat forward: Chromium's DevTools server only
	// binds localhost, which the Go server (different netns) can't reach.
	cdpPort   = SidecarCDPProxyPort
	novncPort = SidecarNoVNCPort

	// cdpWaitTimeout bounds the CDP readiness window. Without this a wedged
	// sidecar kept an HTTP start request hanging until the client gave up
	// ("context canceled" in the server log).
	cdpWaitTimeout = 60 * time.Second

	// relayNamePrefix identifies the loopback relay container (see Start).
	relayNamePrefix = "pcoder-preview-relay-"
)

// DisplayWidth/Height is the sidecar's X screen size. The preview page
// fits the Chromium window to the noVNC iframe (see tools/viewport), so
// this is the cap for auto-fit: big enough for desktop browser windows,
// small enough to stay cheap. window == screen == iframe at 1:1.
const (
	DisplayWidth  = 1920
	DisplayHeight = 1080
)

type containerRuntime interface {
	Run(context.Context, docker.Spec) (string, error)
	Inspect(context.Context, string) (docker.Container, error)
	Stop(context.Context, string, time.Duration) error
	Remove(context.Context, string, bool) error
}

// DockerFactory launches one browser sidecar in the project's network
// namespace (required: dev servers like Vite bind localhost by default, so
// the sidecar reaches the app over the project's loopback). Docker forbids
// publishing ports on a netns-shared container, so how the server reaches
// the sidecar's own CDP/noVNC endpoints depends on the host:
//
//   - Linux: container bridge IPs are routable from the host — the server
//     dials the sidecar IP directly (the original design).
//   - Docker Desktop (macOS/Windows): the engine runs in a VM, so bridge
//     IPs are unreachable from the host. The factory then starts a tiny
//     relay container on pcoder-net (own netns → publishable) whose socat
//     forwards CDP+noVNC to the sidecar IP, published on host loopback with
//     engine-assigned ports. The relay is torn down with the worker.
type DockerFactory struct {
	Docker     containerRuntime
	Image      string
	HTTPClient *http.Client
	PollEvery  time.Duration
	// CDPWait bounds the sidecar readiness window; 0 uses cdpWaitTimeout.
	CDPWait time.Duration
}

func (f *DockerFactory) Start(ctx context.Context, cfg Config) (Worker, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if f.Docker == nil {
		return nil, fmt.Errorf("docker runtime is required")
	}
	image := f.Image
	if image == "" {
		image = DefaultBrowserImage
	}
	if ir, ok := f.Docker.(imageRuntime); ok {
		if err := ensureBrowserImage(ctx, ir, image); err != nil {
			return nil, fmt.Errorf("ensure browser image: %w", err)
		}
	}
	targetURL := "http://127.0.0.1:3000"
	if cfg.Port > 0 {
		targetURL = "http://127.0.0.1:" + strconv.Itoa(cfg.Port)
	}
	spec := docker.Spec{
		Name:     "pcoder-preview-" + cfg.ProjectID,
		Image:    image,
		Writable: true,
		Network:  docker.NetworkNamespace(cfg.ContainerID),
		Env: []string{
			"BROWSER_TARGET=" + targetURL,
			"BROWSER_PROFILE=/tmp/pcoder-browser-profile",
			"WIDTH=" + strconv.Itoa(DisplayWidth), "HEIGHT=" + strconv.Itoa(DisplayHeight),
			"CDP_PORT=" + strconv.Itoa(SidecarChromiumPort),
			"CDP_PROXY_PORT=" + strconv.Itoa(SidecarCDPProxyPort),
			"VNC_WEB_PORT=" + strconv.Itoa(SidecarNoVNCPort),
		},
	}
	cid, err := f.Docker.Run(ctx, spec)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		// A previous sidecar leaked its container (e.g. a failed Stop
		// deregistered the worker but left the runtime behind). Clear the
		// stale same-name container once and retry instead of deadlocking
		// every future preview of this project.
		_ = f.Docker.Remove(ctx, spec.Name, true)
		cid, err = f.Docker.Run(ctx, spec)
	}
	if err != nil {
		return nil, fmt.Errorf("run browser sidecar: %w", err)
	}
	var relayID string
	cleanup := func() {
		_ = f.Docker.Stop(context.Background(), cid, 5*time.Second)
		_ = f.Docker.Remove(context.Background(), cid, true)
		if relayID != "" {
			_ = f.Docker.Stop(context.Background(), relayID, 2*time.Second)
			_ = f.Docker.Remove(context.Background(), relayID, true)
		}
	}
	c, err := f.Docker.Inspect(ctx, cid)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("inspect browser sidecar: %w", err)
	}
	if !c.Running {
		cleanup()
		return nil, fmt.Errorf("browser sidecar exited before CDP became ready (status: %s)", c.Status)
	}

	// Endpoints ride the project container's bridge IP: the sidecar shares
	// its netns, so Inspect reports no IP for the sidecar itself. That IP
	// works from the host on Linux. Not on Docker Desktop — the engine
	// lives in a VM, so the host cannot route to bridge IPs (refused dials
	// fail instantly; unroutable ones black-hole). The probe distinguishes
	// the two, then the relay (see DockerFactory doc) takes over.
	project, err := f.Docker.Inspect(ctx, cfg.ContainerID)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("inspect project container: %w", err)
	}
	if project.NetworkIP == "" {
		cleanup()
		return nil, fmt.Errorf("project container has no network IP; cannot derive sidecar endpoints")
	}
	ep := Endpoint{
		CDP:     "http://" + net.JoinHostPort(project.NetworkIP, strconv.Itoa(cdpPort)),
		Display: "http://" + net.JoinHostPort(project.NetworkIP, strconv.Itoa(novncPort)),
	}
	if !directCDPReady(ctx, f.HTTPClient, ep.CDP, f.PollEvery) {
		var relayEp Endpoint
		relayID, relayEp, err = f.startRelay(ctx, cfg.ProjectID, project.NetworkIP)
		if err != nil {
			cleanup()
			return nil, err
		}
		ep = relayEp
	}
	// A sidecar whose CDP never answers is a wedge, not a wait: bound the
	// readiness window so callers get an error instead of an HTTP request
	// that hangs until the client gives up.
	wait := f.CDPWait
	if wait <= 0 {
		wait = cdpWaitTimeout
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	if err := WaitForCDP(waitCtx, f.HTTPClient, ep.CDP, f.PollEvery); err != nil {
		cleanup()
		return nil, err
	}
	return &dockerWorker{docker: f.Docker, id: cid, relayID: relayID, endpoint: ep}, nil
}

// directCDPReady probes whether the server can complete a CDP version
// request over the engine's bridge network right now (Linux fast path).
// Refused dials (Chromium still booting) fail instantly while unroutable
// addresses (Docker Desktop) black-hole each attempt, so several short
// attempts separate "not up yet" from "never routable".
func directCDPReady(ctx context.Context, client *http.Client, endpoint string, pollEvery time.Duration) bool {
	if client == nil {
		client = http.DefaultClient
	}
	if pollEvery <= 0 {
		pollEvery = 50 * time.Millisecond
	}
	for attempt := 0; attempt < 5; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(pollEvery):
			}
		}
		attemptCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, endpoint+"/json/version", nil)
		if err == nil {
			res, err := client.Do(req)
			if err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					cancel()
					return true
				}
			}
		}
		cancel()
	}
	return false
}

// startRelay starts the Docker Desktop fallback: a container on pcoder-net
// that socat-forwards the sidecar's CDP and noVNC ports to its bridge IP,
// published on the host's loopback with engine-assigned ports (Docker
// rejects publications on netns-shared containers, which is why the relay
// has its own netns). Reuses the browser image: it already ships socat.
func (f *DockerFactory) startRelay(ctx context.Context, projectID, sidecarIP string) (string, Endpoint, error) {
	image := f.Image
	if image == "" {
		image = DefaultBrowserImage
	}
	spec := docker.Spec{
		Name:            relayNamePrefix + projectID,
		Image:           image,
		Network:         docker.DefaultNetwork,
		Entrypoint:      []string{"sh", "-c"},
		PublishLoopback: []int{cdpPort, novncPort},
		Cmd: []string{fmt.Sprintf(
			"socat TCP-LISTEN:%d,fork,reuseaddr TCP:%s:%d & socat TCP-LISTEN:%d,fork,reuseaddr TCP:%s:%d; wait",
			cdpPort, sidecarIP, cdpPort, novncPort, sidecarIP, novncPort,
		)},
	}
	id, err := f.Docker.Run(ctx, spec)
	if err != nil && strings.Contains(err.Error(), "already exists") {
		_ = f.Docker.Remove(ctx, spec.Name, true)
		id, err = f.Docker.Run(ctx, spec)
	}
	if err != nil {
		return "", Endpoint{}, fmt.Errorf("run preview relay: %w", err)
	}
	c, err := f.Docker.Inspect(ctx, id)
	if err != nil {
		_ = f.Docker.Remove(ctx, id, true)
		return "", Endpoint{}, fmt.Errorf("inspect preview relay: %w", err)
	}
	cdpHost, dispHost := c.Published[cdpPort], c.Published[novncPort]
	if !c.Running || cdpHost <= 0 || dispHost <= 0 {
		_ = f.Docker.Remove(ctx, id, true)
		return "", Endpoint{}, fmt.Errorf("preview relay has no loopback publications (cdp %d, display %d)", cdpHost, dispHost)
	}
	return id, Endpoint{
		CDP:     "http://127.0.0.1:" + strconv.Itoa(cdpHost),
		Display: "http://127.0.0.1:" + strconv.Itoa(dispHost),
	}, nil
}

type dockerWorker struct {
	docker   containerRuntime
	id       string
	relayID  string // non-empty on Docker Desktop: the loopback relay dies with the worker
	endpoint Endpoint
}

func (w *dockerWorker) Endpoint() Endpoint { return w.endpoint }

func (w *dockerWorker) Close(ctx context.Context) error {
	var first error
	if w.relayID != "" {
		if err := w.docker.Stop(ctx, w.relayID, 2*time.Second); err != nil && !errors.Is(err, docker.ErrNotFound) {
			first = err
		}
		if err := w.docker.Remove(ctx, w.relayID, true); err != nil && first == nil && !errors.Is(err, docker.ErrNotFound) {
			first = err
		}
	}
	if err := w.docker.Stop(ctx, w.id, 5*time.Second); err != nil && first == nil {
		return err
	}
	if err := w.docker.Remove(ctx, w.id, true); err != nil && first == nil {
		return err
	}
	return first
}

// WaitForCDP waits for Chromium's documented JSON version endpoint. The
// endpoint must contain a websocket debugger URL; a listening TCP port alone
// is not proof that Chromium is ready for automation.
func WaitForCDP(ctx context.Context, client *http.Client, endpoint string, pollEvery time.Duration) error {
	if client == nil {
		client = http.DefaultClient
	}
	if pollEvery <= 0 {
		pollEvery = 50 * time.Millisecond
	}
	url := endpoint + "/json/version"
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err == nil {
			res, requestErr := client.Do(req)
			if requestErr == nil {
				var body struct {
					WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
				}
				decodeErr := json.NewDecoder(res.Body).Decode(&body)
				res.Body.Close()
				if res.StatusCode == http.StatusOK && decodeErr == nil && body.WebSocketDebuggerURL != "" {
					return nil
				}
			}
		}
		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("wait for CDP at %s: %w", endpoint, ctx.Err())
		case <-timer.C:
		}
	}
}
