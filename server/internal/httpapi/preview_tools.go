package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"pcoder/internal/preview"
)

// ── CDP session cache ───────────────────────────────────────────────────

type cdpSession struct {
	ws      *websocket.Conn
	mu      sync.Mutex
	wmu     sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
	// projectID ties the socket to its cache entry so a dead readLoop can
	// self-evict. done is closed when readLoop exits (sidecar died).
	projectID string
	done      chan struct{}
}

var (
	cdpMu    sync.Mutex
	cdpCache = map[string]*cdpSession{}
)

func evictCDP(projectID string) {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if s, ok := cdpCache[projectID]; ok {
		_ = s.ws.Close()
		delete(cdpCache, projectID)
	}
}

// evictCDPIfCurrent removes the entry only if it still points at s, so a
// redialed session is never evicted by a dying predecessor.
func evictCDPIfCurrent(projectID string, s *cdpSession) {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if cur, ok := cdpCache[projectID]; ok && cur == s {
		_ = s.ws.Close()
		delete(cdpCache, projectID)
	}
}

func isSessionDead(s *cdpSession) bool {
	if s.done == nil {
		return false
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func getCDP(projectID, cdpURL string) (*cdpSession, error) {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if s, ok := cdpCache[projectID]; ok {
		if isSessionDead(s) {
			_ = s.ws.Close()
			delete(cdpCache, projectID)
		} else {
			slog.Debug("cdp: reusing cached session", "project", projectID)
			return s, nil
		}
	}
	wsURL, err := cdpWSURL(cdpURL)
	if err != nil {
		return nil, err
	}
	slog.Debug("cdp: dialing", "project", projectID, "wsURL", wsURL)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial CDP ws: %w", err)
	}
	s := &cdpSession{ws: conn, pending: make(map[int]chan json.RawMessage), projectID: projectID, done: make(chan struct{})}
	cdpCache[projectID] = s
	go s.readLoop()
	return s, nil
}

func (s *cdpSession) readLoop() {
	defer close(s.done)
	defer evictCDPIfCurrent(s.projectID, s)
	for {
		_, raw, err := s.ws.ReadMessage()
		if err != nil {
			return
		}
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil || msg.ID == 0 {
			continue
		}
		s.mu.Lock()
		ch, ok := s.pending[msg.ID]
		if ok {
			delete(s.pending, msg.ID)
		}
		s.mu.Unlock()
		if ok {
			ch <- msg.Result
		}
	}
}

func (s *cdpSession) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextID++
	id := s.nextID
	ch := make(chan json.RawMessage, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	req := map[string]any{"id": id, "method": method}
	if params != nil {
		req["params"] = params
	}
	// Gorilla allows one concurrent reader and one writer: the readLoop
	// owns reads, but concurrent HTTP handlers share this writer, so
	// writes must be serialized or the connection panics and dies.
	s.wmu.Lock()
	err := s.ws.WriteJSON(req)
	s.wmu.Unlock()
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		// A failed write means the socket is dead — evict so the next
		// getCDP redials instead of reusing a broken connection forever.
		evictCDPIfCurrent(s.projectID, s)
		return nil, err
	}
	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case result := <-ch:
		return result, nil
	case <-time.After(30 * time.Second):
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("CDP call %s timed out", method)
	}
}

func cdpWSURL(endpoint string) (string, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	// Retry /json/list a few times — the page target may not appear
	// instantly even though WaitForCDP already proved the transport is up.
	// No /json/version fallback: a browser-level target has no page
	// context, so page-dependent tools would fail later anyway. Fail
	// loudly here instead.
	for attempt := 0; attempt < 5; attempt++ {
		resp, err := client.Get(endpoint + "/json/list")
		if err == nil {
			var targets []struct {
				Type                 string `json:"type"`
				WebsocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			decodeErr := json.NewDecoder(resp.Body).Decode(&targets)
			resp.Body.Close()
			if decodeErr == nil {
				for _, t := range targets {
					if t.Type == "page" && t.WebsocketDebuggerURL != "" {
						slog.Debug("cdp: using page target", "url", t.WebsocketDebuggerURL, "attempt", attempt)
						return t.WebsocketDebuggerURL, nil
					}
				}
			}
		}
		time.Sleep(time.Second)
	}
	return "", fmt.Errorf("no page target at %s/json/list", endpoint)
}

// ── Helpers ─────────────────────────────────────────────────────────────

func cdpForRequest(d Deps, r *http.Request) (*cdpSession, error) {
	id := r.PathValue("id")
	worker, err := ensurePreviewWorker(d, r)
	if err != nil {
		return nil, fmt.Errorf("preview not running: %w", err)
	}
	ep := worker.Endpoint()
	slog.Debug("cdp: got worker endpoint", "project", id, "cdp", ep.CDP, "display", ep.Display)
	return getCDP(id, ep.CDP)
}

// previewReadyTimeout bounds the synchronous readiness check in
// handlePreviewStart: Open only lights up for a verified page.
const previewReadyTimeout = 25 * time.Second

// statusPingTimeout bounds the CDP liveness check in handlePreviewStatus.
const statusPingTimeout = 3 * time.Second

// waitForPageReady polls until the sidecar shows the navigated page:
// readyState complete on a non-error URL. A dead port lands on
// chrome-error:// — loaded, but not ours.
func waitForPageReady(ctx context.Context, s *cdpSession, target string) error {
	for {
		raw, err := s.call(ctx, "Runtime.evaluate", map[string]any{
			"expression":    "JSON.stringify({state: document.readyState, href: location.href})",
			"returnByValue": true,
		})
		if err != nil {
			return err
		}
		str, err := decodeEvaluateString(raw)
		if err != nil {
			return err
		}
		var st struct {
			State string `json:"state"`
			Href  string `json:"href"`
		}
		if err := json.Unmarshal([]byte(str), &st); err != nil {
			return err
		}
		if strings.HasPrefix(st.Href, "chrome-error://") {
			return fmt.Errorf("navigation error page")
		}
		// Browsers normalize a bare host with a trailing slash. Dev
		// servers also redirect / to a subpath — accept any complete load
		// on the same origin (host:port) as the target, not just the
		// exact URL, so healthy apps aren't mislabeled as failures.
		if st.State == "complete" && (st.Href == target || st.Href == target+"/" || samePreviewOrigin(target, st.Href)) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// samePreviewOrigin reports whether href is on the same origin (host:port)
// as target, treating localhost and 127.0.0.1 as equivalent loopbacks.
func samePreviewOrigin(target, href string) bool {
	tu, err := url.Parse(target)
	if err != nil || tu.Host == "" {
		return false
	}
	hu, err := url.Parse(href)
	if err != nil || hu.Host == "" {
		return false
	}
	if hu.Scheme != "http" && hu.Scheme != "https" {
		return false
	}
	normalize := func(h string) string {
		host, port, ok := strings.Cut(h, ":")
		if !ok {
			return h
		}
		if host == "localhost" || host == "127.0.0.1" {
			host = "loopback"
		}
		return host + ":" + port
	}
	return normalize(tu.Host) == normalize(hu.Host)
}

// resolveSelector evaluates a JS selector and returns center coordinates.
// A missing selector evaluates to null — that is an error, not a (0,0)
// click in the top-left corner.
func resolveSelector(ctx context.Context, s *cdpSession, selector string) (int, int, error) {
	js := fmt.Sprintf(`(function(){const e=document.querySelector(%q);if(!e)return null;const r=e.getBoundingClientRect();return{x:r.x+r.width/2,y:r.y+r.height/2}})()`, selector)
	result, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		return 0, 0, err
	}
	return decodePoint(result, selector)
}

// errSelectorMiss means CDP answered fine but the selector matched nothing.
var errSelectorMiss = errors.New("selector matched no element")

func decodePoint(raw json.RawMessage, selector string) (int, int, error) {
	var decoded struct {
		Result struct {
			Value *struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return 0, 0, err
	}
	if decoded.Result.Value == nil {
		return 0, 0, fmt.Errorf("%w: %q", errSelectorMiss, selector)
	}
	return int(decoded.Result.Value.X), int(decoded.Result.Value.Y), nil
}

// decodeEvaluateString extracts the string value of a Runtime.evaluate
// returnByValue response. CDP error frames decode to a missing value, which
// used to surface as empty 200 payloads — now a 502.
func decodeEvaluateString(raw json.RawMessage) (string, error) {
	var decoded struct {
		Result struct {
			Value *string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	if decoded.Result.Value == nil {
		return "", fmt.Errorf("cdp returned no string value")
	}
	return *decoded.Result.Value, nil
}

// decodeScreenshotPNG extracts PNG bytes from a captureScreenshot response.
func decodeScreenshotPNG(raw json.RawMessage) ([]byte, error) {
	var decoded struct {
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	if decoded.Data == "" {
		return nil, fmt.Errorf("cdp returned no screenshot data")
	}
	return base64.StdEncoding.DecodeString(decoded.Data)
}

// ── Handlers (func(Deps) http.HandlerFunc pattern) ──────────────────────

func handlePreviewScreenshot(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		result, err := s.call(r.Context(), "Page.captureScreenshot", map[string]any{"format": "png"})
		if err != nil {
			writeInternalErr(w, "cdp screenshot", err)
			return
		}
		pngBytes, err := decodeScreenshotPNG(result)
		if err != nil {
			writeInternalErr(w, "decode screenshot", err)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}
}

func handlePreviewInspect(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		// Simple evaluate — if CDP connected to a page target this returns HTML.
		result, err := s.call(r.Context(), "Runtime.evaluate", map[string]any{
			"expression":    "document.documentElement?.outerHTML || ''",
			"returnByValue": true,
		})
		if err != nil {
			writeInternalErr(w, "cdp inspect", err)
			return
		}
		html, err := decodeEvaluateString(result)
		if err != nil {
			writeInternalErr(w, "decode inspect", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"html": html})
	}
}

func handlePreviewConsole(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		result, err := s.call(r.Context(), "Runtime.evaluate", map[string]any{
			"expression":    "JSON.stringify(window.__pcoder_logs||[])",
			"returnByValue": true,
		})
		if err != nil {
			writeInternalErr(w, "cdp console", err)
			return
		}
		raw, err := decodeEvaluateString(result)
		if err != nil {
			writeInternalErr(w, "decode console", err)
			return
		}
		var logs []string
		if err := json.Unmarshal([]byte(raw), &logs); err != nil {
			writeInternalErr(w, "decode console logs", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"logs": logs})
	}
}

func handlePreviewNetwork(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		result, err := s.call(r.Context(), "Runtime.evaluate", map[string]any{
			"expression":    "JSON.stringify(performance.getEntriesByType('resource').map(e=>({name:e.name,dur:e.duration})))",
			"returnByValue": true,
		})
		if err != nil {
			writeInternalErr(w, "cdp network", err)
			return
		}
		raw, err := decodeEvaluateString(result)
		if err != nil {
			writeInternalErr(w, "decode network", err)
			return
		}
		var entries []map[string]any
		if err := json.Unmarshal([]byte(raw), &entries); err != nil {
			writeInternalErr(w, "decode network entries", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"requests": entries})
	}
}

func handlePreviewNavigate(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			URL string `json:"url"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, err = s.call(r.Context(), "Page.navigate", map[string]any{"url": body.URL})
		if err != nil {
			writeInternalErr(w, "cdp navigate", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

// cdpCaller is the subset of *cdpSession the input helpers need. Kept as
// an interface so unit tests can script press-succeeds/release-fails
// sequences deterministically — killing a real websocket between two
// back-to-back localhost dispatches is not reliably reproducible.
type cdpCaller interface {
	call(ctx context.Context, method string, params any) (json.RawMessage, error)
}

// clickAt dispatches a press+release pair, which CDP cannot do atomically.
// If the release fails after the press landed, the button is left visibly
// pressed (stuck :active) until the next click/navigate. So on release
// failure it makes one best-effort release on the same session to unstick
// the button, then still returns the release error — failing loudly so the
// caller retries instead of assuming a clean click. The retry deliberately
// reuses s rather than redialing: a write failure already evicted the
// shared session (see call), so the next fresh call redials on its own.
func clickAt(ctx context.Context, s cdpCaller, x, y int) error {
	if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mousePressed", "x": x, "y": y, "button": "left", "clickCount": 1,
	}); err != nil {
		return fmt.Errorf("cdp click press: %w", err)
	}
	if _, err := s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1,
	}); err != nil {
		// Best effort: the press already landed, try to leave the button up.
		_, _ = s.call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": x, "y": y, "button": "left", "clickCount": 1,
		})
		return fmt.Errorf("cdp click press succeeded but release failed: %w", err)
	}
	return nil
}

// typeText dispatches keyDown/keyUp per rune. A failed keyUp after a landed
// keyDown leaves a stuck key (e.g. a modifier held), so it gets the same
// best-effort keyUp + loud-error treatment as clickAt's release.
func typeText(ctx context.Context, s cdpCaller, text string) error {
	for _, ch := range text {
		key := string(ch)
		if _, err := s.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "text": key, "key": key}); err != nil {
			return fmt.Errorf("cdp type keyDown: %w", err)
		}
		if _, err := s.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key}); err != nil {
			_, _ = s.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key})
			return fmt.Errorf("cdp type keyDown succeeded but keyUp failed: %w", err)
		}
	}
	return nil
}

func handlePreviewClick(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Selector string `json:"selector"`
			X        int    `json:"x"`
			Y        int    `json:"y"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if body.Selector != "" {
			body.X, body.Y, err = resolveSelector(r.Context(), s, body.Selector)
			if err != nil {
				if errors.Is(err, errSelectorMiss) {
					writeErr(w, http.StatusNotFound, err.Error())
					return
				}
				writeInternalErr(w, "cdp resolve", err)
				return
			}
		}
		if err := clickAt(r.Context(), s, body.X, body.Y); err != nil {
			// clickAt already made a best-effort release when the press
			// landed; keep the half-press note as a stable log op.
			op := "cdp click press"
			if strings.Contains(err.Error(), "release failed") {
				op = "cdp click press succeeded but release failed"
			}
			writeInternalErr(w, op, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handlePreviewType(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Selector string `json:"selector"`
			Text     string `json:"text"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if body.Selector != "" {
			_, err = s.call(r.Context(), "Runtime.evaluate", map[string]any{
				"expression": fmt.Sprintf(`document.querySelector(%q)?.focus()`, body.Selector),
			})
			if err != nil {
				writeInternalErr(w, "cdp type focus", err)
				return
			}
		}
		if err := typeText(r.Context(), s, body.Text); err != nil {
			op := "cdp type keyDown"
			if strings.Contains(err.Error(), "keyUp failed") {
				op = "cdp type keyDown succeeded but keyUp failed"
			}
			writeInternalErr(w, op, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handlePreviewReload(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, err = s.call(r.Context(), "Page.reload", nil)
		if err != nil {
			writeInternalErr(w, "cdp reload", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handlePreviewScroll(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			DeltaX int `json:"deltaX"`
			DeltaY int `json:"deltaY"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, err = s.call(r.Context(), "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseWheel", "deltaX": body.DeltaX, "deltaY": body.DeltaY, "x": 640, "y": 400,
		})
		if err != nil {
			writeInternalErr(w, "cdp scroll", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	}
}

func handlePreviewViewport(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Width  int  `json:"width"`
			Height int  `json:"height"`
			Mobile bool `json:"mobile"`
		}
		if !decodeBody(w, r, &body, false) {
			return
		}
		if body.Width < 100 || body.Height < 100 {
			writeErr(w, http.StatusBadRequest, "width and height must be at least 100")
			return
		}
		// The X screen is fixed (no window manager to grow it), so clamp:
		// the Chromium window can never exceed the framebuffer.
		width := min(body.Width, preview.DisplayWidth)
		height := min(body.Height, preview.DisplayHeight)
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		if err := setWindowBounds(r.Context(), s, width, height); err != nil {
			writeInternalErr(w, "cdp window bounds", err)
			return
		}
		_, err = s.call(r.Context(), "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": width, "height": height,
			"deviceScaleFactor": 1, "mobile": body.Mobile,
		})
		if err != nil {
			writeInternalErr(w, "cdp viewport", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "width": width, "height": height})
	}
}

// setWindowBounds resizes the Chromium app window itself (not just the
// emulated layout viewport) so the 1:1 noVNC view keeps filling the frame
// at any size instead of letterboxing inside a fixed window.
func setWindowBounds(ctx context.Context, s *cdpSession, width, height int) error {
	targetsRaw, err := s.call(ctx, "Target.getTargets", nil)
	if err != nil {
		return err
	}
	var targets struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(targetsRaw, &targets); err != nil {
		return err
	}
	targetID := ""
	for _, t := range targets.TargetInfos {
		if t.Type == "page" {
			targetID = t.TargetID
			break
		}
	}
	if targetID == "" {
		return fmt.Errorf("no page target")
	}
	windowRaw, err := s.call(ctx, "Browser.getWindowForTarget", map[string]any{"targetId": targetID})
	if err != nil {
		return err
	}
	var window struct {
		WindowID int `json:"windowId"`
	}
	if err := json.Unmarshal(windowRaw, &window); err != nil {
		return err
	}
	_, err = s.call(ctx, "Browser.setWindowBounds", map[string]any{
		"windowId": window.WindowID,
		"bounds":   map[string]any{"width": width, "height": height, "windowState": "normal"},
	})
	return err
}
