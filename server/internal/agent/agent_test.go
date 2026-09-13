package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNormalizeBaseURL(t *testing.T) {
	cases := map[string]string{
		"https://api.openai.com/v1":                  "https://api.openai.com/v1",
		"https://api.openai.com/v1/":                 "https://api.openai.com/v1",
		"https://api.openai.com/v1/chat/completions": "https://api.openai.com/v1",
		"  https://x.test/v1/  ":                     "https://x.test/v1",
		"http://localhost:8080":                      "http://localhost:8080",
	}
	for in, want := range cases {
		if got := NormalizeBaseURL(in); got != want {
			t.Errorf("NormalizeBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestConfigValid(t *testing.T) {
	if (Config{}).Valid() {
		t.Errorf("empty config must be invalid")
	}
	if (!Config{BaseURL: "http://x", APIKey: "k", Model: "m"}.Valid()) {
		t.Errorf("full config must be valid")
	}
	if (Config{BaseURL: "http://x", Model: "m"}).Valid() {
		t.Errorf("missing key must be invalid")
	}
}

func TestConnectionRequiresFields(t *testing.T) {
	if err := TestConnection(context.Background(), Config{}); err == nil {
		t.Fatalf("empty config must fail without a model call")
	}
}

func TestConnectionMapsWeakEndpoints(t *testing.T) {
	// A proxy that rejects tools must surface as a tools error, so the
	// settings form blames capability, not the key.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]any{"message": "this model does not support tools"},
		})
	}))
	defer srv.Close()
	err := TestConnection(context.Background(), Config{BaseURL: srv.URL, APIKey: "k", Model: "m"})
	if err == nil || !strings.Contains(err.Error(), "does not support tools") {
		t.Fatalf("err = %v, want tools-support error", err)
	}
}

func TestRunRejectsUnconfigured(t *testing.T) {
	if _, err := Run(context.Background(), Config{}, "sys", "hi", nil, nil, "s", nil, 2, nil); err == nil {
		t.Fatalf("unconfigured run must fail")
	}
}
