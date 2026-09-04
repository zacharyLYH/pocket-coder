package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ── CDP session cache ───────────────────────────────────────────────────

type cdpSession struct {
	ws      *websocket.Conn
	mu      sync.Mutex
	nextID  int
	pending map[int]chan json.RawMessage
}

var (
	cdpMu    sync.Mutex
	cdpCache = map[string]*cdpSession{}
)

func getCDP(projectID, cdpURL string) (*cdpSession, error) {
	cdpMu.Lock()
	defer cdpMu.Unlock()
	if s, ok := cdpCache[projectID]; ok {
		slog.Info("cdp: reusing cached session", "project", projectID)
		return s, nil
	}
	wsURL, err := cdpWSURL(cdpURL)
	if err != nil {
		return nil, err
	}
	slog.Info("cdp: dialing", "project", projectID, "wsURL", wsURL)
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("dial CDP ws: %w", err)
	}
	s := &cdpSession{ws: conn, pending: make(map[int]chan json.RawMessage)}
	cdpCache[projectID] = s
	go s.readLoop()
	return s, nil
}

func (s *cdpSession) readLoop() {
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
	if err := s.ws.WriteJSON(req); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
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
	// Retry /json/list a few times — the page target may not appear instantly.
	for attempt := 0; attempt < 5; attempt++ {
		resp, err := client.Get(endpoint + "/json/list")
		if err == nil {
			defer resp.Body.Close()
			var targets []struct {
				Type                 string `json:"type"`
				WebsocketDebuggerURL string `json:"webSocketDebuggerUrl"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&targets); err == nil {
				for _, t := range targets {
					if t.Type == "page" && t.WebsocketDebuggerURL != "" {
						slog.Info("cdp: using page target", "url", t.WebsocketDebuggerURL, "attempt", attempt)
						return t.WebsocketDebuggerURL, nil
					}
				}
			}
		}
		time.Sleep(time.Second)
	}
	// Fallback to /json/version (browser debugger — no page context)
	slog.Warn("cdp: no page target found, falling back to browser debugger")
	resp2, err := client.Get(endpoint + "/json/version")
	if err != nil {
		return "", err
	}
	defer resp2.Body.Close()
	var body struct {
		WebsocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.WebsocketDebuggerURL == "" {
		return "", fmt.Errorf("no webSocketDebuggerUrl")
	}
	return body.WebsocketDebuggerURL, nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

func cdpForRequest(d Deps, r *http.Request) (*cdpSession, error) {
	id := r.PathValue("id")
	worker, err := ensurePreviewWorker(d, r)
	if err != nil {
		return nil, fmt.Errorf("preview not running: %w", err)
	}
	ep := worker.Endpoint()
	slog.Info("cdp: got worker endpoint", "project", id, "cdp", ep.CDP, "display", ep.Display)
	return getCDP(id, ep.CDP)
}

// resolveSelector evaluates a JS selector and returns center coordinates.
func resolveSelector(ctx context.Context, s *cdpSession, selector string) (int, int, error) {
	js := fmt.Sprintf(`(function(){const e=document.querySelector(%q);if(!e)return null;const r=e.getBoundingClientRect();return{x:r.x+r.width/2,y:r.y+r.height/2}})()`, selector)
	result, err := s.call(ctx, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true})
	if err != nil {
		return 0, 0, err
	}
	var decoded struct {
		Result struct {
			Value struct {
				X float64 `json:"x"`
				Y float64 `json:"y"`
			} `json:"value"`
		} `json:"result"`
	}
	_ = json.Unmarshal(result, &decoded)
	return int(decoded.Result.Value.X), int(decoded.Result.Value.Y), nil
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
		var decoded struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(result, &decoded)
		pngBytes, err := base64.StdEncoding.DecodeString(decoded.Data)
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
		var decoded struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(result, &decoded)
		writeJSON(w, http.StatusOK, map[string]any{"html": decoded.Result.Value})
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
			"expression":    "JSON.stringify(window.__sps_logs||[])",
			"returnByValue": true,
		})
		if err != nil {
			writeInternalErr(w, "cdp console", err)
			return
		}
		var decoded struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(result, &decoded)
		var logs []string
		_ = json.Unmarshal([]byte(decoded.Result.Value), &logs)
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
		var decoded struct {
			Result struct {
				Value string `json:"value"`
			} `json:"result"`
		}
		_ = json.Unmarshal(result, &decoded)
		var entries []map[string]any
		_ = json.Unmarshal([]byte(decoded.Result.Value), &entries)
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
				writeInternalErr(w, "cdp resolve", err)
				return
			}
		}
		_, _ = s.call(r.Context(), "Input.dispatchMouseEvent", map[string]any{
			"type": "mousePressed", "x": body.X, "y": body.Y, "button": "left", "clickCount": 1,
		})
		_, _ = s.call(r.Context(), "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseReleased", "x": body.X, "y": body.Y, "button": "left", "clickCount": 1,
		})
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
			_, _ = s.call(r.Context(), "Runtime.evaluate", map[string]any{
				"expression": fmt.Sprintf(`document.querySelector(%q)?.focus()`, body.Selector),
			})
		}
		for _, ch := range body.Text {
			key := string(ch)
			_, _ = s.call(r.Context(), "Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "text": key, "key": key})
			_, _ = s.call(r.Context(), "Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": key})
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
		if body.Width < 100 || body.Width > 4096 || body.Height < 100 || body.Height > 4096 {
			writeErr(w, http.StatusBadRequest, "width and height must be between 100 and 4096")
			return
		}
		s, err := cdpForRequest(d, r)
		if err != nil {
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		_, err = s.call(r.Context(), "Emulation.setDeviceMetricsOverride", map[string]any{
			"width": body.Width, "height": body.Height,
			"deviceScaleFactor": 1, "mobile": body.Mobile,
		})
		if err != nil {
			writeInternalErr(w, "cdp viewport", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "width": body.Width, "height": body.Height})
	}
}
