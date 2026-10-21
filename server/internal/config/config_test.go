package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "./data" || cfg.Bind != ":8080" || cfg.DockerSock != "unix:///var/run/docker.sock" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadAllFields(t *testing.T) {
	cfg, err := load([]string{
		"PCODER_DATA_DIR=/srv/pcoder",
		"PCODER_BIND=127.0.0.1:9000",
		"PCODER_JWT_SECRET=" + strings.Repeat("x", 32),
		"PCODER_DOCKER_SOCK=tcp://127.0.0.1:2375",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "/srv/pcoder" || cfg.Bind != "127.0.0.1:9000" ||
		cfg.JWTSecret != strings.Repeat("x", 32) ||
		cfg.DockerSock != "tcp://127.0.0.1:2375" {
		t.Fatalf("got %+v", *cfg)
	}
}

func TestLoadAllowAnyRepoHatch(t *testing.T) {
	cfg, err := load(nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AllowAnyRepo {
		t.Fatalf("AllowAnyRepo defaults to %+v, want false", cfg.AllowAnyRepo)
	}
	cfg, err = load([]string{"PCODER_ALLOW_ANY_REPO=1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.AllowAnyRepo {
		t.Fatal("PCODER_ALLOW_ANY_REPO=1 must set AllowAnyRepo")
	}
}

// The login email moved into state.json (written by set-password). A stale
// shell export must fail loudly instead of being silently ignored, or an
// operator would think it still configures the account.
func TestLoadRejectsRetiredAuthEmail(t *testing.T) {
	_, err := load([]string{"PCODER_AUTH_EMAIL=me@example.com"})
	if err == nil || !strings.Contains(err.Error(), "PCODER_AUTH_EMAIL") {
		t.Fatalf("expected unknown-variable error, got %v", err)
	}
}

// No auth backdoor: retired SMTP variables are ignored — they map to no
// Config field, so they can never create or configure a login.
func TestLoadIgnoresSMTPVariables(t *testing.T) {
	cfg, err := load([]string{"SMTP_HOST=smtp.gmail.com", "SMTP_PASSWORD=x", "SMTP_USER=a@b.c"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "./data" || cfg.Bind != ":8080" {
		t.Fatalf("SMTP variables changed config: %+v", *cfg)
	}
}

func TestLoadRejectsUnknownVariable(t *testing.T) {
	for _, unknown := range []string{"PCODER_BOGUS_SETTING=1"} {
		if _, err := load([]string{unknown}); err == nil || !strings.Contains(err.Error(), strings.SplitN(unknown, "=", 2)[0]) {
			t.Fatalf("expected unknown-variable error for %q, got %v", unknown, err)
		}
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{"bad bind", []string{"PCODER_BIND=8080"}},
		{"short jwt secret", []string{"PCODER_JWT_SECRET=short"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(tc.env); err == nil {
				t.Fatalf("expected error for %v", tc.env)
			}
		})
	}
}

// Config is process-env only. A leftover .env in the working directory (or
// its parent) must not influence anything — that was the old contract.
func TestLoadIgnoresDotEnvFiles(t *testing.T) {
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PCODER_") {
			t.Skipf("real environment shadows the test: %s", kv)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("PCODER_DATA_DIR=/from/dotenv\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "server")
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(sub)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "./data" {
		t.Fatalf("DataDir = %q, want the default (dotenv must be ignored)", cfg.DataDir)
	}
}
