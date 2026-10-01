package main

import (
	"net"
	"testing"

	"pcoder/internal/config"
)

func testSMTPConfig() *config.Config {
	return &config.Config{
		LoginEmail: "user@example.com",
		SMTPHost:   "127.0.0.1", SMTPPort: 1,
		SMTPUser: "user@example.com", SMTPPass: "secret", SMTPFrom: "user@example.com",
	}
}

func TestRunSMTPTestNeedsCredentials(t *testing.T) {
	cfg := testSMTPConfig()
	cfg.SMTPUser, cfg.SMTPPass = "", ""
	if err := runSMTPTest(cfg); err == nil {
		t.Fatal("runSMTPTest without credentials = nil, want error")
	}
}

func TestRunSMTPTestSurfacesDialFailure(t *testing.T) {
	// Nothing listens on a closed port: the dial must fail fast and the
	// error must propagate (the setup script dies on exactly this).
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfg := testSMTPConfig()
	cfg.SMTPPort = port
	if err := runSMTPTest(cfg); err == nil {
		t.Fatal("runSMTPTest against dead port = nil, want error")
	}
}
