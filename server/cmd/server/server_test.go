package main

import (
	"net/http"
	"testing"
	"time"
)

// The zero-value http.Server enforces no transport timeouts, so a client
// can dribble headers forever (Slowloris) and hold a goroutine per
// connection. This pins the constructor's bounds: WriteTimeout must stay
// above the longest synchronous handler (10-min Docker builds), while
// reads stay tight (bodies are small JSON).
func TestNewHTTPServerSetsTransportTimeouts(t *testing.T) {
	srv := newHTTPServer(":8080", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 10*time.Second {
		t.Fatalf("ReadHeaderTimeout = %v, want 10s", srv.ReadHeaderTimeout)
	}
	if srv.ReadTimeout != 60*time.Second {
		t.Fatalf("ReadTimeout = %v, want 60s", srv.ReadTimeout)
	}
	if srv.WriteTimeout < 10*time.Minute {
		t.Fatalf("WriteTimeout = %v, must exceed the 10-min exec/build ceiling", srv.WriteTimeout)
	}
	if srv.IdleTimeout == 0 {
		t.Fatal("IdleTimeout unset: idle keep-alive connections never close")
	}
	if srv.MaxHeaderBytes <= 0 {
		t.Fatal("MaxHeaderBytes unset: unbounded header memory per connection")
	}
}
