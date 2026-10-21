package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/hkdf"
)

func TestOpenFreshCreatesFiles(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.PublicKey() == "" {
		t.Fatal("empty public key")
	}
	for _, name := range []string{vapidFile, pushFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("%s not created: %v", name, err)
		}
	}
	if got := s.List(); len(got) != 0 {
		t.Fatalf("subs = %d, want 0", len(got))
	}
}

func TestDeletionSemantics(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := s.PublicKey()
	if err := s.Subscribe(context.Background(), Subscription{Endpoint: "https://fcm.googleapis.com/x", Keys: Keys{P256dh: "x", Auth: "y"}}); err != nil {
		t.Fatal(err)
	}

	// push.json only: key stable, store empty.
	if err := os.Remove(filepath.Join(dir, pushFile)); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.PublicKey() != oldKey {
		t.Fatal("vapid key changed when only push.json was removed")
	}
	if len(s.List()) != 0 {
		t.Fatal("store not empty after push.json removal")
	}

	// vapid.json only: key changes, subscriptions clear.
	if err := s.Subscribe(context.Background(), Subscription{Endpoint: "https://fcm.googleapis.com/x"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, vapidFile)); err != nil {
		t.Fatal(err)
	}
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.PublicKey() == oldKey {
		t.Fatal("vapid key did not rotate")
	}
	if len(s.List()) != 0 {
		t.Fatal("subscriptions survived rotation")
	}
}

func TestCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, vapidFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err == nil {
		t.Fatal("corrupt vapid.json must fail boot")
	}

	dir = t.TempDir()
	if _, err := Open(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, pushFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, pushFile+".corrupt")); err != nil {
		t.Fatal("corrupt push.json was not quarantined")
	}
	if len(s.List()) != 0 {
		t.Fatal("quarantined store should start empty")
	}
}

func TestValidateSubscription(t *testing.T) {
	pub, auth := validKeys(t)
	ok := Subscription{Endpoint: "https://fcm.googleapis.com/fcm/send/x", Keys: Keys{P256dh: pub, Auth: auth}}
	if err := ValidateSubscription(ok); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	cases := map[string]Subscription{
		"http":         {Endpoint: "http://fcm.googleapis.com/x", Keys: ok.Keys},
		"unknown host": {Endpoint: "https://evil.example/x", Keys: ok.Keys},
		"bad key":      {Endpoint: "https://fcm.googleapis.com/x", Keys: Keys{P256dh: "x", Auth: auth}},
		"bad auth":     {Endpoint: "https://fcm.googleapis.com/x", Keys: Keys{P256dh: pub, Auth: "x"}},
	}
	for name, sub := range cases {
		if err := ValidateSubscription(sub); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if err := ValidateSubscription(Subscription{Endpoint: "http://host.docker.internal:9/x", Keys: ok.Keys}); err != nil {
		t.Fatalf("local test relay rejected: %v", err)
	}
}

func TestSendEncryptsAndDecrypts(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	priv, sub := testSubscription(t)
	var body []byte
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got == "" {
			t.Error("missing VAPID Authorization header")
		}
		if got := r.Header.Get("Content-Encoding"); got != "aes128gcm" {
			t.Errorf("Content-Encoding = %q", got)
		}
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
	}))
	defer relay.Close()
	sub.Endpoint = relay.URL + "/push"
	if err := s.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	res, err := s.Send(context.Background(), Message{Title: "hi", Body: "there", Navigate: "https://x/app", Tag: "t"})
	if err != nil || res.Sent != 1 {
		t.Fatalf("send = %+v, %v", res, err)
	}
	plain := decrypt(t, priv, sub, body)
	var p payload
	if err := json.Unmarshal(plain, &p); err != nil {
		t.Fatal(err)
	}
	if p.WebPush != 8030 || p.Notification.Title != "hi" || p.Notification.Body != "there" ||
		p.Notification.Navigate != "https://x/app" || p.Notification.Tag != "t" {
		t.Fatalf("payload = %+v", p)
	}
}

func TestSendPruneAndVAPID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, sub := testSubscription(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer dead.Close()
	sub.Endpoint = dead.URL + "/gone"
	if err := s.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	res, err := s.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Pruned != 1 || len(s.List()) != 0 {
		t.Fatalf("res=%+v subs=%d", res, len(s.List()))
	}

	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer auth.Close()
	sub.Endpoint = auth.URL + "/auth"
	if err := s.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(context.Background(), Message{Title: "t", Body: "b"}); !errors.Is(err, ErrVAPID) {
		t.Fatalf("want ErrVAPID, got %v", err)
	}
	if len(s.List()) != 1 {
		t.Fatal("auth failure must not prune")
	}
}

func TestSendRetries429(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, sub := testSubscription(t)
	calls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer relay.Close()
	sub.Endpoint = relay.URL + "/push"
	if err := s.Subscribe(context.Background(), sub); err != nil {
		t.Fatal(err)
	}
	res, err := s.Send(context.Background(), Message{Title: "t", Body: "b"})
	if err != nil || res.Sent != 1 {
		t.Fatalf("send = %+v, %v", res, err)
	}
}

func validKeys(t *testing.T) (p256dh, auth string) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(secret)
}

func testSubscription(t *testing.T) (*ecdh.PrivateKey, Subscription) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, 16)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	return priv, Subscription{Keys: Keys{
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(secret),
	}}
}

// decrypt implements the receiving half of RFC 8291 / RFC 8188 for one record.
func decrypt(t *testing.T, priv *ecdh.PrivateKey, sub Subscription, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatal("body too short")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idLen := int(body[20])
	if len(body) < 21+idLen {
		t.Fatal("body too short for keyid")
	}
	asPub, err := ecdh.P256().NewPublicKey(body[21 : 21+idLen])
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := body[21+idLen:]
	shared, err := priv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := base64.RawURLEncoding.DecodeString(sub.Keys.Auth)
	prk := hkdf.Extract(sha256.New, shared, auth)
	info := append([]byte("WebPush: info\x00"), priv.PublicKey().Bytes()...)
	info = append(info, asPub.Bytes()...)
	ikm := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, prk, info), ikm); err != nil {
		t.Fatal(err)
	}
	keyPRK := hkdf.Extract(sha256.New, ikm, salt)
	cek := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, keyPRK, []byte("Content-Encoding: aes128gcm\x00")), cek); err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(hkdf.Expand(sha256.New, keyPRK, []byte("Content-Encoding: nonce\x00")), nonce); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) == 0 || plain[len(plain)-1] != 0x02 {
		end := len(plain)
		for end > 0 && plain[end-1] == 0 {
			end--
		}
		if end == 0 || plain[end-1] != 0x02 {
			t.Fatalf("bad padding delimiter (rs=%d)", rs)
		}
		return plain[:end-1]
	}
	return plain[:len(plain)-1]
}
