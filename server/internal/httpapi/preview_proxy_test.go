package httpapi

import (
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestPreviewProxyStripsPCODERPrefix(t *testing.T) {
	target, err := url.Parse("http://10.0.0.8:6080")
	if err != nil {
		t.Fatal(err)
	}
	proxy := newPreviewProxy(target, "websockify")
	req := httptest.NewRequest("GET", "/api/projects/p1/preview/websockify?token=x", nil)
	proxy.Director(req)
	if req.URL.String() != "http://10.0.0.8:6080/websockify?token=x" {
		t.Fatalf("forwarded URL = %s", req.URL)
	}
	if req.Host != "10.0.0.8:6080" {
		t.Fatalf("forwarded host = %q", req.Host)
	}
}
