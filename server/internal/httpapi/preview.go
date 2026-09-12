package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	pathpkg "path"
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
		// "ready" means the browser actually answers: ping CDP, report
		// degraded if dead.
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
		// Synchronous readiness: a dead target fails loudly here.
		ctx, cancel := context.WithTimeout(r.Context(), previewReadyTimeout)
		defer cancel()
		if err := waitForPageReady(ctx, s, target); err != nil {
			writeErr(w, http.StatusBadGateway, "preview target never became ready")
			return
		}
		plog(d, r.PathValue("id"), "preview.start", "Preview started on :"+strconv.Itoa(body.Port), map[string]any{"port": body.Port})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "port": body.Port, "status": "ready"})
	}
}

func handlePreviewClose(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Clear cached CDP session first: it dangles if the worker is gone
		// (stop/restart/delete raced close), and a stale entry makes the next
		// tools call dial a dead socket.
		evictCDP(id)
		if err := d.Preview.Stop(r.Context(), id); err != nil {
			if errors.Is(err, preview.ErrNotFound) {
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": "stopped"})
				return
			}
			writeInternalErr(w, "stop preview", err)
			return
		}
		plog(d, id, "preview.close", "Preview closed", nil)
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
		if hasDotDotSegment(path) {
			writeErr(w, http.StatusBadRequest, "invalid preview path")
			return
		}
		// One event per page open: the noVNC entry document loads once per
		// visit, while its assets and the websockify stream share this
		// handler and would spam the log.
		if path == "vnc.html" || path == "vnc_lite.html" {
			plog(d, r.PathValue("id"), "preview.open", "Preview opened", nil)
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
		// Clean the spliced path so ".." segments can never escape the
		// sidecar's web root upstream, even if a caller bypasses the
		// handler-level rejection (defense in depth: the upstream static
		// server must only ever see a rooted, normalized path).
		req.URL.Path = pathpkg.Clean("/" + strings.TrimPrefix(path, "/"))
		req.URL.RawPath = ""
		req.Host = target.Host
	}
	return proxy
}

// hasDotDotSegment reports whether path contains a ".." segment, encoded
// or not. The noVNC surface only serves known assets; anything climbing
// the tree is rejected before it reaches the reverse proxy.
func hasDotDotSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		unesc, err := url.PathUnescape(seg)
		if err != nil {
			return true // unparseable escaping is not a legit asset name
		}
		if unesc == ".." {
			return true
		}
	}
	return false
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
			// Return an error rather than an empty list so the UI keeps its
			// last-known ports instead of blinking them away.
			slog.Warn("preview ports probe failed", "project", id, "err", err)
			writeErr(w, http.StatusBadGateway, "ports probe failed")
			return
		}
		ports := parseListeningPorts(output)
		writeJSON(w, http.StatusOK, map[string]any{"ports": ports})
	}
}

// sidecarPorts belong to the preview browser itself (see preview package
// Sidecar*Port constants). The sidecar shares the project's network
// namespace, so ss lists them; showing them would invite previewing the
// previewer. A user service that binds one of these ports collides with the
// sidecar and is hidden by design — pick another port for app servers.
var sidecarPorts = map[int]bool{
	preview.SidecarVNCPort:      true,
	preview.SidecarNoVNCPort:    true,
	preview.SidecarChromiumPort: true,
	preview.SidecarCDPProxyPort: true,
}

// dockerEmbeddedDNS is Docker's embedded DNS resolver, an artifact of the
// network namespace, never a user server.
const dockerEmbeddedDNS = "127.0.0.11"

// parseListeningPorts extracts port numbers from ss/netstat listening output.
// Only LISTEN-state lines count: ESTABLISHED flows carry remote ports that
// are not servers, and offering them in the Preview tab (or auto-starting
// against them) points the sidecar at dead ports. Only the Local Address
// column is read so Peer addresses and process names can never contribute
// phantom ports.
func parseListeningPorts(output string) []map[string]any {
	seen := map[int]bool{}
	var slots []map[string]any
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "State") || strings.HasPrefix(line, "Netid") {
			continue
		}
		if !strings.Contains(line, "LISTEN") {
			continue // ESTABLISHED/TIME_WAIT/etc: not a server
		}
		fields := strings.Fields(line)
		addrs := fields
		if len(fields) > 1 {
			// ss and netstat both place Local Address at index 3:
			// ss:      LISTEN Recv-Q Send-Q Local Peer Process
			// netstat: tcp Recv-Q Send-Q Local Foreign State
			if len(fields) < 4 {
				continue
			}
			addrs = fields[3:4]
		}
		for _, field := range addrs {
			if strings.Contains(field, dockerEmbeddedDNS+":") {
				continue // Docker's embedded DNS resolver, never a user server
			}
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
