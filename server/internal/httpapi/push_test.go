package httpapi

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pcoder/internal/push"
)

// pushDeps wires a real store so the tests cover validation, persistence and
// the sender together, never a mocked store.
func pushDeps(t *testing.T) Deps {
	t.Helper()
	d, dataDir := newTestDepsInDir(t)
	s, err := push.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	d.Push = s
	return d
}

func subBody(t *testing.T, endpoint string) string {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{
		"endpoint": endpoint,
		"keys": map[string]string{
			"p256dh": base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
			"auth":   base64.RawURLEncoding.EncodeToString(auth),
		},
	})
	return string(raw)
}

func TestPushSubscribeAndList(t *testing.T) {
	d := pushDeps(t)
	h := New(d)

	rec := get(t, h, "/api/push/key")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"publicKey"`) {
		t.Fatalf("key: %d %s", rec.Code, rec.Body)
	}

	cookie := loginCookie(t, h)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer relay.Close()

	if rec := authedPost(t, h, cookie, "/api/push/subscribe", subBody(t, relay.URL+"/push")); rec.Code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	// Upsert, not append.
	if rec := authedPost(t, h, cookie, "/api/push/subscribe", subBody(t, relay.URL+"/push")); rec.Code != http.StatusOK {
		t.Fatalf("resubscribe: %d %s", rec.Code, rec.Body)
	}
	rec = authedGet(t, h, cookie, "/api/push/subscriptions")
	var got struct {
		Subscriptions []struct {
			Endpoint  string `json:"endpoint"`
			CreatedAt string `json:"createdAt"`
			Keys      any    `json:"keys"`
		} `json:"subscriptions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Subscriptions) != 1 || got.Subscriptions[0].Endpoint != relay.URL+"/push" {
		t.Fatalf("list = %+v", got)
	}
}

func TestPushSubscribeRejectsBadEndpoints(t *testing.T) {
	d := pushDeps(t)
	h := New(d)
	cookie := loginCookie(t, h)
	for _, endpoint := range []string{
		"https://evil.example/push",
		"http://fcm.googleapis.com/push",
		"ftp://fcm.googleapis.com/push",
	} {
		rec := authedPost(t, h, cookie, "/api/push/subscribe", subBody(t, endpoint))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: got %d, want 400", endpoint, rec.Code)
		}
	}
}

func TestPushSendValidationAndPrune(t *testing.T) {
	d := pushDeps(t)
	h := New(d)
	cookie := loginCookie(t, h)

	for _, body := range []string{
		`{"title":"","body":"b"}`,
		`{"title":"t","body":""}`,
		`{"title":"` + strings.Repeat("x", maxTitle+1) + `","body":"b"}`,
		`{"title":"t","body":"` + strings.Repeat("x", maxBody+1) + `"}`,
		`{"title":"t","body":"b","tag":"` + strings.Repeat("x", maxTag+1) + `"}`,
		`{"title":"t","body":"b","navigate":"https://evil.example/app"}`,
	} {
		if rec := authedPost(t, h, cookie, "/api/push", body); rec.Code != http.StatusBadRequest {
			t.Fatalf("%.40s: got %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
	}

	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer relay.Close()
	if rec := authedPost(t, h, cookie, "/api/push/subscribe", subBody(t, relay.URL+"/push")); rec.Code != http.StatusOK {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	rec := authedPost(t, h, cookie, "/api/push", `{"title":"t","body":"b"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pruned":1`) {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	if rec := authedGet(t, h, cookie, "/api/push/subscriptions"); !strings.Contains(rec.Body.String(), `"subscriptions":[]`) {
		t.Fatalf("dead subscription survived: %s", rec.Body)
	}
}
