package httpapi

import (
	"context"
	"net/http"
	"testing"

	"sps/internal/preview"
)

type previewTestWorker struct{}

func (previewTestWorker) Endpoint() preview.Endpoint {
	return preview.Endpoint{CDP: "http://private:9222", Display: "http://private:6080"}
}
func (previewTestWorker) Close(context.Context) error { return nil }

type previewTestFactory struct{}

func (previewTestFactory) Start(context.Context, preview.Config) (preview.Worker, error) {
	return previewTestWorker{}, nil
}

func TestPreviewStatusRequiresAuthAndNeverReturnsPrivateEndpoint(t *testing.T) {
	d, pinOut := newTestDeps(t)
	m := preview.NewManager(previewTestFactory{})
	if _, err := m.Ensure(context.Background(), preview.Config{ProjectID: "p1", ContainerID: "sps-p1"}); err != nil {
		t.Fatal(err)
	}
	d.Preview = m
	h := New(d)
	if rec := get(t, h, "/api/projects/p1/preview"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", rec.Code)
	}
	cookie := loginCookie(t, h, pinOut)
	rec := authedGet(t, h, cookie, "/api/projects/p1/preview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); body == "" || containsAny(body, "private:9222", "private:6080") {
		t.Fatalf("private endpoint leaked in response: %s", body)
	}
}

func containsAny(s string, values ...string) bool {
	for _, value := range values {
		if len(value) > 0 && indexOf(s, value) >= 0 {
			return true
		}
	}
	return false
}

func indexOf(s, value string) int {
	for i := 0; i+len(value) <= len(s); i++ {
		if s[i:i+len(value)] == value {
			return i
		}
	}
	return -1
}
