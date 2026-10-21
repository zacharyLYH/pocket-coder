// Web push handlers: key exchange, subscription upsert/list, and the open
// send API. The store owns persistence; handlers own wire validation.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"pcoder/internal/obs"
	"pcoder/internal/push"
)

const (
	maxTitle    = 100
	maxBody     = 500
	maxTag      = 64
	maxNavigate = 2048
	maxPayload  = 3500
)

func handlePushKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"publicKey": d.Push.PublicKey()})
	}
}

func handlePushSubscribe(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var sub push.Subscription
		if !decodeBody(w, r, &sub, false) {
			return
		}
		if err := push.ValidateSubscription(sub); err != nil {
			obs.Warn(r.Context(), obs.PushSubscribe, "rejected push subscription", map[string]any{"error": err})
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := d.Push.Subscribe(r.Context(), sub); err != nil {
			writeInternalErr(w, "save push subscription", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func handlePushList(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		type device struct {
			Endpoint  string `json:"endpoint"`
			CreatedAt string `json:"createdAt"`
		}
		subs := d.Push.List()
		out := make([]device, 0, len(subs))
		for _, sub := range subs {
			out = append(out, device{Endpoint: sub.Endpoint, CreatedAt: sub.CreatedAt})
		}
		writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out})
	}
}

func handlePushSend(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var msg push.Message
		if !decodeBody(w, r, &msg, false) {
			return
		}
		msg.Title = strings.TrimSpace(msg.Title)
		msg.Body = strings.TrimSpace(msg.Body)
		if msg.Title == "" || msg.Body == "" {
			obs.Warn(r.Context(), obs.PushSend, "rejected push: title and body are required", nil)
			writeErr(w, http.StatusBadRequest, "title and body are required")
			return
		}
		if len(msg.Title) > maxTitle || len(msg.Body) > maxBody ||
			len(msg.Tag) > maxTag || len(msg.Navigate) > maxNavigate {
			obs.Warn(r.Context(), obs.PushSend, "rejected push: payload too large", nil)
			writeErr(w, http.StatusBadRequest, "payload too large")
			return
		}
		navigate, err := normalizeNavigate(r, msg.Navigate)
		if err != nil {
			obs.Warn(r.Context(), obs.PushSend, "rejected push: bad navigate", map[string]any{"error": err})
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		msg.Navigate = navigate
		if raw, err := json.Marshal(msg); err != nil || len(raw) > maxPayload {
			obs.Warn(r.Context(), obs.PushSend, "rejected push: payload too large", nil)
			writeErr(w, http.StatusBadRequest, "payload too large")
			return
		}

		res, err := d.Push.Send(r.Context(), msg)
		body := map[string]any{"sent": res.Sent, "failed": res.Failed, "pruned": res.Pruned}
		if errors.Is(err, push.ErrVAPID) {
			body["error"] = "push relay rejected VAPID credentials"
			writeJSON(w, http.StatusBadGateway, body)
			return
		}
		if err != nil {
			writeInternalErr(w, "send push", err)
			return
		}
		writeJSON(w, http.StatusOK, body)
	}
}

// normalizeNavigate defaults to the app root and rejects foreign origins.
// The request's own Host is the public origin, matching origin.go's trust
// model (the proxy forwards Host).
func normalizeNavigate(r *http.Request, raw string) (string, error) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		if first, _, ok := strings.Cut(proto, ","); ok {
			proto = first
		}
		if p := strings.ToLower(strings.TrimSpace(proto)); p == "http" || p == "https" {
			scheme = p
		}
	}
	if raw == "" {
		return scheme + "://" + r.Host + "/app", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", errors.New("invalid navigate URL")
	}
	if !strings.EqualFold(u.Scheme, scheme) || !strings.EqualFold(u.Host, r.Host) {
		return "", errors.New("navigate must be same-origin")
	}
	return raw, nil
}
