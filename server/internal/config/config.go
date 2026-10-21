// Package config loads and validates server configuration from the
// environment. Server-specific variables carry the PCODER_ prefix so that
// "unknown config" is detectable: any PCODER_* variable we do not
// know about is a startup error rather than a silently ignored typo.
//
// There is no .env loading. Config comes from the process environment
// only, and the login email is not config at all: it lives in state.json
// with the password hash, so the app has one identity source.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Environment variable names, exported so any package can reference config
// keys by name. Everything has a sensible default.
const (
	EnvDataDir    = "PCODER_DATA_DIR"    // where all state lives; default ./data
	EnvBind       = "PCODER_BIND"        // listen address; default :8080
	EnvJWTSecret  = "PCODER_JWT_SECRET"  // signing key; auto-generated + persisted when unset
	EnvDockerSock = "PCODER_DOCKER_SOCK" // docker engine endpoint; default unix:///var/run/docker.sock
	EnvAIBaseURL  = "PCODER_AI_BASE_URL" // OpenAI-compatible base URL for the codemap agent
	EnvAPIKey     = "PCODER_AI_API_KEY"  // single global model key; empty disables codemaps
	EnvAIModel    = "PCODER_AI_MODEL"    // model name sent with chat completions

	// EnvAllowAnyRepo lifts the GitHub-only create requirement for
	// test stacks that clone from a local git daemon (e2e, live-engine
	// integration tests). Never set in production: it exists so tests
	// can exercise the pipeline without touching github.com.
	EnvAllowAnyRepo = "PCODER_ALLOW_ANY_REPO"

	// EnvPreviewSweepInterval bounds how stale token presence may look.
	EnvPreviewSweepInterval = "PCODER_PREVIEW_SWEEP_INTERVAL"
	// EnvPreviewTokenSilence bounds quiet time before token rotation.
	EnvPreviewTokenSilence = "PCODER_PREVIEW_TOKEN_SILENCE"
)

// envKeys is the recognized set, derived from the constants above.
var envKeys = []string{
	EnvDataDir, EnvBind, EnvJWTSecret, EnvDockerSock,
	EnvAllowAnyRepo, EnvAIBaseURL, EnvAPIKey, EnvAIModel,
	EnvPreviewSweepInterval, EnvPreviewTokenSilence,
}

// knownKeys backs the unknown-variable check so a typo fails startup
// instead of being silently ignored.
var knownKeys = func() map[string]bool {
	m := make(map[string]bool, len(envKeys))
	for _, k := range envKeys {
		m[k] = true
	}
	return m
}()

// Config holds everything the server needs to boot. Fields map 1:1 to
// environment variables; empty fields mean "not set".
type Config struct {
	DataDir    string
	Bind       string
	JWTSecret  string
	DockerSock string
	AIBaseURL  string
	AIAPIKey   string
	AIModel    string
	// AllowAnyRepo lifts the GitHub-only create requirement. Test stacks
	// only (see EnvAllowAnyRepo); production leaves it false.
	AllowAnyRepo bool
}

// Load reads configuration from the process environment.
func Load() (*Config, error) {
	return load(os.Environ())
}

func load(env []string) (*Config, error) {
	values := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "PCODER_") {
			if !knownKeys[key] {
				return nil, fmt.Errorf("unknown config variable %q (supported: %s)", key, known())
			}
			values[key] = value
		}
	}

	cfg := &Config{
		DataDir:    strEnv(values, "PCODER_DATA_DIR", "./data"),
		Bind:       strEnv(values, "PCODER_BIND", ":8080"),
		JWTSecret:  values["PCODER_JWT_SECRET"],
		DockerSock: strEnv(values, "PCODER_DOCKER_SOCK", "unix:///var/run/docker.sock"),
		AIBaseURL:  values["PCODER_AI_BASE_URL"],
		AIAPIKey:   values["PCODER_AI_API_KEY"],
		AIModel:    values["PCODER_AI_MODEL"],
		AllowAnyRepo: values["PCODER_ALLOW_ANY_REPO"] == "1" ||
			values["PCODER_ALLOW_ANY_REPO"] == "true",
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks that set values are usable, failing loudly on anything
// that would misbehave at runtime.
func (c *Config) Validate() error {
	if c.DataDir == "" {
		return fmt.Errorf("PCODER_DATA_DIR must not be empty")
	}
	if _, port, err := net.SplitHostPort(c.Bind); err != nil || port == "" {
		return fmt.Errorf("PCODER_BIND %q is not a host:port address: %v", c.Bind, err)
	}
	if c.JWTSecret != "" && len(c.JWTSecret) < 32 {
		return fmt.Errorf("PCODER_JWT_SECRET must be at least 32 characters when set")
	}
	return nil
}

func strEnv(values map[string]string, key, def string) string {
	if v, ok := values[key]; ok && v != "" {
		return v
	}
	return def
}

func known() string {
	return strings.Join(envKeys, ", ")
}
