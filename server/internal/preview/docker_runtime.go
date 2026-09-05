package preview

import (
	"context"
	"encoding/json"
	"fmt"
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
	SidecarCDPProxyPort = 9223 // socat forward (CDP_PROXY_PORT, reachable from server netns)
	SidecarNoVNCPort    = 6080 // websockify (VNC_WEB_PORT)

	// CDP goes through the socat forward: Chromium's DevTools server only
	// binds localhost, which the Go server (different netns) can't reach.
	cdpPort   = SidecarCDPProxyPort
	novncPort = SidecarNoVNCPort
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
// namespace. The sidecar has no host-published ports; the server reaches it
// through the private PCODER Docker network using its container IP.
type DockerFactory struct {
	Docker     containerRuntime
	Image      string
	HTTPClient *http.Client
	PollEvery  time.Duration
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
	cleanup := func() {
		_ = f.Docker.Stop(context.Background(), cid, 5*time.Second)
		_ = f.Docker.Remove(context.Background(), cid, true)
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
	// A container using network_mode=container:<project> has no network
	// address of its own. The project owns the namespace and its address.
	if c.NetworkIP == "" {
		c, err = f.Docker.Inspect(ctx, cfg.ContainerID)
		if err != nil {
			cleanup()
			return nil, fmt.Errorf("inspect project network: %w", err)
		}
	}
	if c.NetworkIP == "" {
		cleanup()
		return nil, fmt.Errorf("project container has no private network address")
	}
	ep := Endpoint{CDP: "http://" + c.NetworkIP + ":" + strconv.Itoa(cdpPort), Display: "http://" + c.NetworkIP + ":" + strconv.Itoa(novncPort)}
	if err := WaitForCDP(ctx, f.HTTPClient, ep.CDP, f.PollEvery); err != nil {
		cleanup()
		return nil, err
	}
	return &dockerWorker{docker: f.Docker, id: cid, endpoint: ep}, nil
}

type dockerWorker struct {
	docker   containerRuntime
	id       string
	endpoint Endpoint
}

func (w *dockerWorker) Endpoint() Endpoint { return w.endpoint }

func (w *dockerWorker) Close(ctx context.Context) error {
	if err := w.docker.Stop(ctx, w.id, 5*time.Second); err != nil {
		return err
	}
	return w.docker.Remove(ctx, w.id, true)
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
