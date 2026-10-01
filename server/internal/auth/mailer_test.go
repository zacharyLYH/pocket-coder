package auth

import (
	"context"
	"net"
	"testing"
	"time"
)

// A black-holed SMTP endpoint: accepts and then never speaks. The dial
// succeeds instantly (backlog), so only a real deadline — not the dial
// itself — can bound the call. Without one this test hangs until the go
// test timeout kills it.
func TestSendTestHonorsContextDeadline(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()
	m := SmtpMailer{
		Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port,
		User: "u@example.com", Password: "secret", From: "u@example.com",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := m.SendTest(ctx, "to@example.com", "probe"); err == nil {
		t.Fatal("SendTest against stalled server = nil, want error")
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("SendTest took %v against a stalled server, want deadline enforcement", took)
	}
}
