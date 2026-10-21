// Package push owns web push: the VAPID keypair, the subscription store, and
// the sender. One account per instance, so subscriptions are keyed by endpoint
// and there is no user dimension.
package push

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"pcoder/internal/obs"
)

const (
	vapidFile = "vapid.json"
	pushFile  = "push.json"

	// Subscriber is the VAPID `sub` claim, a required contact URI. webpush-go
	// prepends "mailto:" unless the value starts with "https:", so pass a bare
	// address: "mailto:..." would become "mailto:mailto:...", which Apple
	// rejects with 403 BadJwtToken (FCM and Mozilla tolerate it).
	Subscriber = "leeyihong03@gmail.com"

	sendTimeout = 10 * time.Second
	ttlSeconds  = 60
)

// ErrVAPID means the relay rejected our VAPID credentials. It is a
// configuration error, not a dead subscription, so nothing is pruned.
var ErrVAPID = errors.New("push relay rejected VAPID credentials")

// Keys is the browser's PushSubscription.toJSON().keys verbatim.
type Keys struct {
	P256dh string `json:"p256dh"`
	Auth   string `json:"auth"`
}

// Subscription is one device's push subscription.
type Subscription struct {
	Endpoint  string `json:"endpoint"`
	Keys      Keys   `json:"keys"`
	CreatedAt string `json:"createdAt"`
}

// Message is the send input. Empty optional fields are omitted from the
// declarative payload.
type Message struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	Navigate string `json:"navigate,omitempty"`
	Tag      string `json:"tag,omitempty"`
}

// Result is the fan-out summary.
type Result struct {
	Sent   int `json:"sent"`
	Failed int `json:"failed"`
	Pruned int `json:"pruned"`
}

type vapid struct {
	PublicKey  string `json:"publicKey"`
	PrivateKey string `json:"privateKey"`
}

type file struct {
	Subscriptions map[string]Subscription `json:"subscriptions"`
}

// Store holds the VAPID keypair and subscriptions in memory, persisted as
// two JSON files in the data dir.
type Store struct {
	mu   sync.Mutex
	dir  string
	key  vapid
	subs map[string]Subscription
}

// Open loads or creates vapid.json and push.json. A missing vapid.json is a
// deliberate rotation (or first install): keys are regenerated and every
// stored subscription is dropped. A corrupt vapid.json fails boot; a corrupt
// push.json is quarantined.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create push data dir: %w", err)
	}
	s := &Store{dir: dir, subs: map[string]Subscription{}}
	raw, err := os.ReadFile(s.path(vapidFile))
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return nil, fmt.Errorf("read %s: %w", vapidFile, err)
	}
	if err == nil {
		if err := json.Unmarshal(raw, &s.key); err != nil || s.key.PublicKey == "" || s.key.PrivateKey == "" {
			return nil, fmt.Errorf("invalid %s: delete it to rotate", vapidFile)
		}
	}
	if err := s.loadSubscriptionsLocked(); err != nil {
		return nil, err
	}
	if missing {
		dropped := len(s.subs)
		if err := s.regenerateLocked(); err != nil {
			return nil, err
		}
		obs.Info(context.Background(), obs.PushRotate, "generated VAPID keypair", map[string]any{"dir": dir, "dropped_subscriptions": dropped})
	}
	return s, nil
}

// PublicKey returns the VAPID application server key clients subscribe with.
func (s *Store) PublicKey() string { return s.key.PublicKey }

// Wipe rotates the VAPID keypair and drops every stored subscription. Every
// device must enable notifications again with the new application server key.
func (s *Store) Wipe(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := len(s.subs)
	if err := s.regenerateLocked(); err != nil {
		return err
	}
	obs.Info(ctx, obs.PushRotate, "rotated VAPID keypair and cleared subscriptions", map[string]any{
		"dir": s.dir, "dropped_subscriptions": dropped,
	})
	return nil
}

// Subscribe upserts by endpoint.
func (s *Store) Subscribe(ctx context.Context, sub Subscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.Endpoint = strings.TrimSpace(sub.Endpoint)
	if sub.CreatedAt == "" {
		sub.CreatedAt = time.Now().UTC().Format(time.RFC3339)
	}
	s.subs[sub.Endpoint] = sub
	if err := s.saveSubscriptionsLocked(); err != nil {
		obs.Error(ctx, obs.PushSubscribe, "save push subscription failed", map[string]any{"host": endpointHost(sub.Endpoint), "error": err})
		return err
	}
	obs.Info(ctx, obs.PushSubscribe, "push subscription stored", map[string]any{"host": endpointHost(sub.Endpoint), "subscriptions": len(s.subs)})
	return nil
}

// List returns subscriptions in stable endpoint order.
func (s *Store) List() []Subscription {
	out, _ := s.snapshot()
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint < out[j].Endpoint })
	return out
}

// snapshot copies the current subscriptions and key under the lock, so
// network sends never hold it.
func (s *Store) snapshot() ([]Subscription, vapid) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Subscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out, s.key
}

type payload struct {
	WebPush      int     `json:"web_push"`
	Notification Message `json:"notification"`
}

// Send fans out to every subscription, best-effort. A relay auth failure is
// returned after the loop; one device's failure never stops the rest.
func (s *Store) Send(ctx context.Context, m Message) (Result, error) {
	body, err := json.Marshal(payload{WebPush: 8030, Notification: m})
	if err != nil {
		return Result{}, err
	}

	subs, key := s.snapshot()
	obs.Info(ctx, obs.PushSend, "sending push", map[string]any{
		"subscriptions": len(subs), "tag": m.Tag, "navigate": m.Navigate,
	})

	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	var res Result
	var dead []string
	var authErr error
	for _, sub := range subs {
		host := endpointHost(sub.Endpoint)
		status, err := s.send(ctx, body, sub, key, false)
		if err == nil && status == http.StatusTooManyRequests {
			obs.Warn(ctx, obs.PushSend, "push relay rate limited", map[string]any{"host": host})
			status, err = s.send(ctx, body, sub, key, true)
		}
		switch {
		case err != nil:
			res.Failed++
			obs.Warn(ctx, obs.PushSend, "push delivery failed", map[string]any{"host": host, "error": err})
		case status >= 200 && status < 300:
			res.Sent++
		case status == http.StatusNotFound || status == http.StatusGone:
			res.Pruned++
			dead = append(dead, sub.Endpoint)
			obs.Info(ctx, obs.PushSend, "push endpoint is gone", map[string]any{"host": host, "status": status})
		case status == http.StatusUnauthorized || status == http.StatusForbidden:
			res.Failed++
			authErr = fmt.Errorf("%w: relay returned %d", ErrVAPID, status)
			obs.Error(ctx, obs.PushSend, "push relay rejected VAPID credentials", map[string]any{"host": host, "status": status})
		case status == http.StatusRequestEntityTooLarge:
			res.Failed++
			obs.Warn(ctx, obs.PushSend, "push payload rejected as too large", map[string]any{"host": host, "status": status})
		default:
			res.Failed++
			obs.Warn(ctx, obs.PushSend, "push relay returned an error", map[string]any{"host": host, "status": status})
		}
	}
	if len(dead) > 0 {
		s.prune(ctx, dead)
	}
	obs.Info(ctx, obs.PushSend, "push fan-out complete", map[string]any{
		"sent": res.Sent, "failed": res.Failed, "pruned": res.Pruned,
	})
	return res, authErr
}

func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return u.Host
}

func (s *Store) send(ctx context.Context, payload []byte, sub Subscription, key vapid, retry bool) (int, error) {
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.Keys.P256dh, Auth: sub.Keys.Auth},
	}, &webpush.Options{
		Subscriber:      Subscriber,
		VAPIDPublicKey:  key.PublicKey,
		VAPIDPrivateKey: key.PrivateKey,
		TTL:             ttlSeconds,
	})
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if retry && resp.StatusCode == http.StatusTooManyRequests {
		return 0, errors.New("still rate limited")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			delay := time.Duration(secs) * time.Second
			if delay > 2*time.Second {
				delay = 2 * time.Second
			}
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		}
	}
	return resp.StatusCode, nil
}

func (s *Store) prune(ctx context.Context, endpoints []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ep := range endpoints {
		delete(s.subs, ep)
	}
	if err := s.saveSubscriptionsLocked(); err != nil {
		obs.Error(ctx, obs.PushPrune, "save pruned subscriptions failed", map[string]any{"error": err})
		return
	}
	obs.Info(ctx, obs.PushPrune, "pruned dead push subscriptions", map[string]any{"count": len(endpoints), "remaining": len(s.subs)})
}

func (s *Store) regenerateLocked() error {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return fmt.Errorf("generate VAPID keys: %w", err)
	}
	s.key = vapid{PublicKey: publicKey, PrivateKey: privateKey}
	if err := s.write(vapidFile, s.key); err != nil {
		return err
	}
	s.subs = map[string]Subscription{}
	return s.saveSubscriptionsLocked()
}

func (s *Store) loadSubscriptionsLocked() error {
	raw, err := os.ReadFile(s.path(pushFile))
	if errors.Is(err, os.ErrNotExist) {
		return s.saveSubscriptionsLocked()
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", pushFile, err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		// Subscriptions are recoverable from clients; never block boot.
		obs.Warn(context.Background(), obs.PushStore, "quarantining corrupt push.json", map[string]any{"dir": s.dir, "error": err})
		_ = os.Remove(s.path(pushFile) + ".corrupt")
		_ = os.Rename(s.path(pushFile), s.path(pushFile)+".corrupt")
		s.subs = map[string]Subscription{}
		return s.saveSubscriptionsLocked()
	}
	if f.Subscriptions != nil {
		s.subs = f.Subscriptions
	}
	return nil
}

func (s *Store) saveSubscriptionsLocked() error {
	return s.write(pushFile, file{Subscriptions: s.subs})
}

// write persists v atomically: temp file + rename, 0600 like state.json.
func (s *Store) write(name string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(name) + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(name))
}

func (s *Store) path(name string) string { return filepath.Join(s.dir, name) }

// ValidateSubscription rejects anything we must not POST to. Endpoints come
// from an authenticated owner, but they are still an SSRF surface. Loopback
// and the Docker host may use http so local fake relays work in tests.
func ValidateSubscription(sub Subscription) error {
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Hostname() == "" {
		return errors.New("invalid endpoint")
	}
	host := strings.ToLower(u.Hostname())
	local := isLocalHost(host)
	if u.Scheme != "https" && !(local && u.Scheme == "http") {
		return errors.New("endpoint must be https")
	}
	if !local && !knownRelay(host) {
		return errors.New("unknown push service")
	}
	p256dh, err := decodeBase64(sub.Keys.P256dh)
	x, _ := elliptic.Unmarshal(elliptic.P256(), p256dh)
	if err != nil || len(p256dh) != 65 || p256dh[0] != 4 || x == nil {
		return errors.New("invalid p256dh key")
	}
	auth, err := decodeBase64(sub.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return errors.New("invalid auth secret")
	}
	return nil
}

func decodeBase64(s string) ([]byte, error) {
	if b, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(s)
}

func isLocalHost(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "host.docker.internal":
		return true
	}
	return false
}

func knownRelay(host string) bool {
	for _, suffix := range []string{
		"googleapis.com", "push.apple.com", "mozilla.com", "mozaws.net", "notify.windows.com",
	} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
