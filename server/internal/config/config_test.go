package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := load([]string{"PCODER_AUTH_EMAIL=me@example.com"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "./data" || cfg.Bind != ":8080" || cfg.DockerSock != "unix:///var/run/docker.sock" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.AuthEmail != "me@example.com" {
		t.Fatalf("auth email = %q", cfg.AuthEmail)
	}
}

// The account identifier is normalized at load, exactly like setup.sh
// normalizes it before writing .env — login comparisons depend on it.
func TestLoadNormalizesEmail(t *testing.T) {
	_, err := load([]string{"PCODER_AUTH_EMAIL=  Owner@Example.COM ", "PCODER_AUTH_EMAIL_KEY_UNUSED="})
	if err == nil {
		t.Fatal("unknown PCODER_AUTH_EMAIL_KEY_UNUSED must be rejected")
	}
	cfg, err := load([]string{"PCODER_AUTH_EMAIL=  Owner@Example.COM "})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthEmail != "owner@example.com" {
		t.Fatalf("AuthEmail = %q, want owner@example.com", cfg.AuthEmail)
	}
}

func TestLoadAllFields(t *testing.T) {
	cfg, err := load([]string{
		"PCODER_DATA_DIR=/srv/pcoder",
		"PCODER_BIND=127.0.0.1:9000",
		"PCODER_AUTH_EMAIL=me@example.com",
		"PCODER_JWT_SECRET=" + strings.Repeat("x", 32),
		"PCODER_DOCKER_SOCK=tcp://127.0.0.1:2375",
	})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.DataDir != "/srv/pcoder" || cfg.Bind != "127.0.0.1:9000" ||
		cfg.AuthEmail != "me@example.com" || cfg.JWTSecret != strings.Repeat("x", 32) ||
		cfg.DockerSock != "tcp://127.0.0.1:2375" {
		t.Fatalf("got %+v", *cfg)
	}
}

func TestLoadAllowAnyRepoHatch(t *testing.T) {
	cfg, err := load([]string{"PCODER_AUTH_EMAIL=me@example.com"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AllowAnyRepo {
		t.Fatalf("AllowAnyRepo defaults to %+v, want false", cfg.AllowAnyRepo)
	}
	cfg, err = load([]string{"PCODER_AUTH_EMAIL=me@example.com", "PCODER_ALLOW_ANY_REPO=1"})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.AllowAnyRepo {
		t.Fatalf("PCODER_ALLOW_ANY_REPO=1 must set AllowAnyRepo")
	}
}

func TestLoadRejectsMissingAuthEmail(t *testing.T) {
	_, err := load(nil)
	if err == nil || !strings.Contains(err.Error(), "PCODER_AUTH_EMAIL") {
		t.Fatalf("expected missing-email error, got %v", err)
	}
}

// No auth backdoor: old SMTP variables are unknown config, not silently
// ignored leftovers.
func TestLoadRejectsSMTPVariables(t *testing.T) {
	for _, smtp := range []string{"SMTP_HOST=smtp.gmail.com", "SMTP_PASSWORD=x", "SMTP_USER=a@b.c"} {
		if _, err := load([]string{smtp}); err == nil {
			t.Fatalf("expected unknown-variable error for %q", smtp)
		}
	}
}

func TestLoadRejectsUnknownVariable(t *testing.T) {
	for _, unknown := range []string{"PCODER_BOGUS_SETTING=1"} {
		if _, err := load([]string{unknown}); err == nil || !strings.Contains(err.Error(), strings.SplitN(unknown, "=", 2)[0]) {
			t.Fatalf("expected unknown-variable error for %q, got %v", unknown, err)
		}
	}
}

func TestAppendDotEnv(t *testing.T) {
	dir := t.TempDir()
	dotenv := filepath.Join(dir, ".env")
	if err := os.WriteFile(dotenv, []byte(`
# comment line
PCODER_AUTH_EMAIL=from-dotenv@example.com
PCODER_BIND = "127.0.0.1:9999"
=no-name
KEY_NO_EQ
`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := []string{"PCODER_BIND=:8080", "PCODER_AUTH_EMAIL=real@example.com"}
	if err := appendDotEnv(&env, dotenv); err != nil {
		t.Fatalf("appendDotEnv: %v", err)
	}

	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	// Real environment wins.
	if got["PCODER_AUTH_EMAIL"] != "real@example.com" {
		t.Fatalf("real env lost: %v", got)
	}
	if got["PCODER_BIND"] != ":8080" {
		t.Fatalf("real env lost for bind: %v", got)
	}
	// Dotenv fills gaps and trims quotes/whitespace.
	if got["PCODER_AUTH_EMAIL"] == "" {
		t.Fatalf("dotenv value missing: %v", got)
	}
}

func TestAppendDotEnvMissingFile(t *testing.T) {
	env := []string{"PCODER_AUTH_EMAIL=me@example.com"}
	if err := appendDotEnv(&env, filepath.Join(t.TempDir(), "nope.env")); err != nil {
		t.Fatalf("missing file should be ignored, got %v", err)
	}
	if len(env) != 1 {
		t.Fatalf("env mutated: %v", env)
	}
}

// skipIfRealEnvConflicts guards Load()-level tests: a developer machine or CI
// runner with PCODER_* variables set would shadow every .env value.
func skipIfRealEnvConflicts(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PCODER_") {
			t.Skipf("real environment shadows .env: %s", kv)
		}
	}
}

func TestLoadFallsBackToParentDotEnv(t *testing.T) {
	skipIfRealEnvConflicts(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"),
		[]byte("PCODER_AUTH_EMAIL=root@example.com\nPCODER_DATA_DIR=/from/root\n"), 0o600); err != nil {
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
	if cfg.AuthEmail != "root@example.com" || cfg.DataDir != "/from/root" {
		t.Fatalf("../.env ignored: %+v", cfg)
	}
}

func TestLoadPrefersLocalDotEnvOverParent(t *testing.T) {
	skipIfRealEnvConflicts(t)
	root := t.TempDir()
	for _, f := range []struct{ path, body string }{
		{filepath.Join(root, ".env"), "PCODER_AUTH_EMAIL=parent@example.com"},
		{filepath.Join(root, "server", ".env"), "PCODER_AUTH_EMAIL=local@example.com"},
	} {
		if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.path, []byte(f.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(filepath.Join(root, "server"))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.AuthEmail != "local@example.com" {
		t.Fatalf("auth email = %q, want the local .env to win", cfg.AuthEmail)
	}
}

func TestValidateRejectsBadValues(t *testing.T) {
	email := "PCODER_AUTH_EMAIL=me@example.com"
	cases := []struct {
		name string
		env  []string
	}{
		{"bad bind", []string{email, "PCODER_BIND=8080"}},
		{"bad email", []string{"PCODER_AUTH_EMAIL=not-an-email"}},
		{"short jwt secret", []string{email, "PCODER_JWT_SECRET=short"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := load(tc.env); err == nil {
				t.Fatalf("expected error for %v", tc.env)
			}
		})
	}
}
