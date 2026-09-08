package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"pcoder/internal/preview"
	"pcoder/internal/project"
)

func handlePreviewStatus(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		worker, err := d.Preview.Get(id)
		if errors.Is(err, preview.ErrNotFound) {
			writeJSON(w, http.StatusOK, map[string]any{"project": id, "status": "stopped"})
			return
		}
		if err != nil {
			writeInternalErr(w, "get preview", err)
			return
		}
		// "ready" means the browser actually answers, not just that a
		// worker record exists: ping CDP briefly, report degraded if dead.
		ep := worker.Endpoint()
		s, err := getCDP(id, ep.CDP)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"project": id, "status": "degraded"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), statusPingTimeout)
		defer cancel()
		if _, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": "1", "returnByValue": true}); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"project": id, "status": "degraded"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"project": id, "status": "ready"})
	}
}

func handlePreviewStart(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Port int `json:"port"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if body.Port < 1 || body.Port > 65535 {
			writeErr(w, http.StatusBadRequest, "invalid port")
			return
		}
		worker, err := ensurePreviewWorker(d, r, body.Port)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) || errors.Is(err, preview.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "project not found")
				return
			}
			writeInternalErr(w, "ensure preview", err)
			return
		}
		ep := worker.Endpoint()
		s, err := getCDP(r.PathValue("id"), ep.CDP)
		if err != nil {
			writeInternalErr(w, "cdp connect", err)
			return
		}
		target := "http://127.0.0.1:" + strconv.Itoa(body.Port)
		raw, err := s.call(r.Context(), "Page.navigate", map[string]any{"url": target})
		if err != nil {
			writeInternalErr(w, "cdp navigate", err)
			return
		}
		var nav struct {
			ErrorText string `json:"errorText"`
		}
		if err := json.Unmarshal(raw, &nav); err != nil {
			writeInternalErr(w, "decode navigate", err)
			return
		}
		if nav.ErrorText != "" {
			writeErr(w, http.StatusBadGateway, nav.ErrorText)
			return
		}
		// Synchronous readiness: a failed/dead target fails loudly here.
		ctx, cancel := context.WithTimeout(r.Context(), previewReadyTimeout)
		defer cancel()
		if err := waitForPageReady(ctx, s, target); err != nil {
			writeErr(w, http.StatusBadGateway, "preview target never became ready")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "port": body.Port, "status": "ready"})
	}
}

func handlePreviewClose(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Clear cached CDP session first: it dangles whenever the worker
		// is already gone (stop/restart/delete raced close), and a stale
		// entry makes the next tools call dial a dead socket.
		evictCDP(id)
		if err := d.Preview.Stop(r.Context(), id); err != nil {
			if errors.Is(err, preview.ErrNotFound) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "stopped"})
				return
			}
			writeInternalErr(w, "stop preview", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "stopped"})
	}
}

// handlePreviewSurface forwards noVNC assets and its websocket through the
// authenticated PCODER origin. The worker endpoint is private and never placed
// in a Location header or JSON response.
func handlePreviewSurface(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		worker, err := ensurePreviewWorker(d, r)
		if err != nil {
			if errors.Is(err, project.ErrNotFound) || errors.Is(err, preview.ErrNotFound) {
				writeErr(w, http.StatusNotFound, "preview is not running")
				return
			}
			writeInternalErr(w, "get preview surface", err)
			return
		}
		ep := worker.Endpoint()
		target, err := url.Parse(ep.Display)
		if err != nil || target.Scheme != "http" || target.Host == "" {
			if err == nil {
				err = errors.New("invalid private display endpoint")
			}
			writeInternalErr(w, "parse preview surface endpoint", err)
			return
		}
		prefix := "/api/projects/" + r.PathValue("id") + "/preview/"
		path := strings.TrimPrefix(r.URL.Path, prefix)
		if path == "" {
			path = "vnc.html"
		}
		proxy := newPreviewProxy(target, path)
		proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, proxyErr error) {
			writeInternalErr(w, "proxy preview surface", proxyErr)
		}
		proxy.ServeHTTP(w, r)
	}
}

func newPreviewProxy(target *url.URL, path string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.URL.Path = "/" + strings.TrimPrefix(path, "/")
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	return proxy
}

// handlePreviewPorts probes listening ports inside the project container.
func handlePreviewPorts(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if d.Projects == nil {
			writeErr(w, http.StatusNotFound, "no projects")
			return
		}
		if _, _, err := d.Projects.Get(r.Context(), id); err != nil {
			writeErr(w, http.StatusNotFound, "no such project")
			return
		}
		cid := project.ContainerName(id)
		output, err := d.Sessions.ExecCommand(r.Context(), cid, "ss -tlnp 2>/dev/null || netstat -tlnp 2>/dev/null || true")
		if err != nil {
			// Probe failure is not "no ports": return an error so the UI
			// keeps its last-known list instead of blinking ports away.
			slog.Warn("preview ports probe failed", "project", id, "err", err)
			writeErr(w, http.StatusBadGateway, "ports probe failed")
			return
		}
		ports := parseListeningPorts(output)
		writeJSON(w, http.StatusOK, map[string]any{"ports": ports})
	}
}

// sidecarPorts belong to the preview browser itself (see preview package
// Sidecar*Port constants and preview/image/start-browser). The sidecar
// shares the project's network namespace, so ss lists them; showing them
// would invite previewing the previewer, and auto-start could even pick one.
// NOTE: a user service that binds one of these ports collides with the
// sidecar and is hidden by design — pick another port for app servers.
var sidecarPorts = map[int]bool{
	preview.SidecarVNCPort:      true,
	preview.SidecarNoVNCPort:    true,
	preview.SidecarChromiumPort: true,
	preview.SidecarCDPProxyPort: true,
}

// dockerEmbeddedDNS is the address Docker's embedded DNS resolver listens
// on in every container network namespace. It is an artifact of the
// namespace, not a user server.
const dockerEmbeddedDNS = "127.0.0.11"

// parseListeningPorts extracts port numbers from ss/netstat output.
func parseListeningPorts(output string) []map[string]any {
	seen := map[int]bool{}
	var slots []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Netid") {
			continue
		}
		if strings.Contains(line, dockerEmbeddedDNS+":") {
			continue // Docker's embedded DNS resolver, never a user server
		}
		for _, field := range strings.Fields(line) {
			if i := strings.LastIndex(field, ":"); i > 0 {
				p, err := strconv.Atoi(field[i+1:])
				if err == nil && p > 0 && p < 65536 && !seen[p] && !sidecarPorts[p] {
					seen[p] = true
					slots = append(slots, map[string]any{"port": p, "status": "live"})
				}
			}
		}
	}
	return slots
}

func ensurePreviewWorker(d Deps, r *http.Request, port ...int) (preview.Worker, error) {
	id := r.PathValue("id")
	if d.Projects == nil {
		return d.Preview.Get(id)
	}
	if _, _, err := d.Projects.Get(r.Context(), id); err != nil {
		return nil, err
	}
	cfg := preview.Config{
		ProjectID: id, ContainerID: project.ContainerName(id),
	}
	if len(port) > 0 && port[0] > 0 {
		cfg.Port = port[0]
	}
	return d.Preview.Ensure(r.Context(), cfg)
}
