package main

import (
	"fmt"
	"strings"
	"testing"

	"pcoder/internal/config"
	"pcoder/internal/state"
)

// A fresh state (email only, no credential) must fail boot instead of
// serving an instance nobody can log in to.
func TestNewAuthServicePanicsWithoutPassword(t *testing.T) {
	dataDir := t.TempDir()
	st, err := state.Open(dataDir, state.Bootstrap{AuthEmail: "owner@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DataDir: dataDir, AuthEmail: "owner@example.com"}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("newAuthService did not panic without a configured password")
		}
		if msg := fmt.Sprint(r); !strings.Contains(msg, "no login password configured") {
			t.Fatalf("panic = %q", msg)
		}
	}()
	_, _ = newAuthService(cfg, st)
}
