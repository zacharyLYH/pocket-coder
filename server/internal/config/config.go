// Package config loads and validates server configuration from the
// environment. Server-specific variables carry the PCODER_ prefix so that
// "unknown config" is detectable: any PCODER_* or SMTP_* variable we do not
// know about is a startup error rather than a silently ignored typo.
package config

import (
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Environment variable names, exported so any package can reference config
// keys by name. Required: PCODER_LOGIN_EMAIL plus the SMTP_* group (documented
// in the README). Everything else has a sensible default.
const (
	EnvDataDir    = "PCODER_DATA_DIR"    // where all state lives; default ./data
	EnvBind       = "PCODER_BIND"        // listen address; default :8080
	EnvLoginEmail = "PCODER_LOGIN_EMAIL" // recipient of login PINs (required)
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

	EnvSMTPHost = "SMTP_HOST"     // Google SMTP by default
	EnvSMTPPort = "SMTP_PORT"     // 587 by default
	EnvSMTPUser = "SMTP_USER"     // Gmail address (required for email)
	EnvSMTPPass = "SMTP_PASSWORD" // Gmail app password (required for email)
	EnvSMTPFrom = "SMTP_FROM"     // sender address (required for email)
)

// envKeys is the recognized set, derived from the constants above.
var envKeys = []string{
	EnvDataDir, EnvBind, EnvLoginEmail, EnvJWTSecret, EnvDockerSock,
	EnvSMTPHost, EnvSMTPPort, EnvSMTPUser, EnvSMTPPass, EnvSMTPFrom,
	EnvAllowAnyRepo, EnvAIBaseURL, EnvAPIKey, EnvAIModel,
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
	LoginEmail string
	JWTSecret  string
	SMTPHost   string
	SMTPPort   int
	SMTPUser   string
	SMTPPass   string
	SMTPFrom   string
	DockerSock string
	AIBaseURL  string
	AIAPIKey   string
	AIModel    string
	// AllowAnyRepo lifts the GitHub-only create requirement. Test stacks
	// only (see EnvAllowAnyRepo); production leaves it false.
	AllowAnyRepo bool
}

// Load reads configuration from the process environment, layering a `.env`
// file underneath (working directory, then its parent — the repo root when
// running inside server/) so host runs work without exporting anything.
// Real environment variables always win, so compose and CI behavior is
// unchanged.
func Load() (*Config, error) {
	env := os.Environ()
	if err := appendDotEnv(&env, ".env"); err != nil {
		return nil, err
	}
	if err := appendDotEnv(&env, "../.env"); err != nil {
		return nil, err
	}
	return load(env)
}

// appendDotEnv parses a dotenv-style file (KEY=VALUE lines, `#` comments,
// optional surrounding quotes) and appends its keys to env, except for keys
// the real environment already provides.
func appendDotEnv(env *[]string, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	present := map[string]bool{}
	for _, kv := range *env {
		if k, _, ok := strings.Cut(kv, "="); ok {
			present[k] = true
		}
	}
	var added []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		if k != "" && !present[k] {
			added = append(added, k+"="+v)
		}
	}
	*env = append(*env, added...)
	return nil
}

func load(env []string) (*Config, error) {
	values := map[string]string{}
	for _, kv := range env {
		key, value, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "PCODER_") || strings.HasPrefix(key, "SMTP_") {
			if !knownKeys[key] {
				return nil, fmt.Errorf("unknown config variable %q (supported: %s)", key, known())
			}
			values[key] = value
		}
	}

	port, err := intEnv(values, "SMTP_PORT", 587)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		DataDir:    strEnv(values, "PCODER_DATA_DIR", "./data"),
		Bind:       strEnv(values, "PCODER_BIND", ":8080"),
		LoginEmail: values["PCODER_LOGIN_EMAIL"],
		JWTSecret:  values["PCODER_JWT_SECRET"],
		SMTPHost:   strEnv(values, "SMTP_HOST", "smtp.gmail.com"),
		SMTPPort:   port,
		SMTPUser:   values["SMTP_USER"],
		SMTPPass:   values["SMTP_PASSWORD"],
		SMTPFrom:   values["SMTP_FROM"],
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
	if c.LoginEmail == "" {
		return fmt.Errorf("PCODER_LOGIN_EMAIL must be set in .env (see README for the required variables)")
	}
	if !emailRe.MatchString(c.LoginEmail) {
		return fmt.Errorf("PCODER_LOGIN_EMAIL %q is not a valid email address", c.LoginEmail)
	}
	if c.JWTSecret != "" && len(c.JWTSecret) < 32 {
		return fmt.Errorf("PCODER_JWT_SECRET must be at least 32 characters when set")
	}
	if c.SMTPPort != 0 && (c.SMTPPort < 1 || c.SMTPPort > 65535) {
		return fmt.Errorf("SMTP_PORT %d is not a valid port", c.SMTPPort)
	}
	return nil
}

var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func strEnv(values map[string]string, key, def string) string {
	if v, ok := values[key]; ok && v != "" {
		return v
	}
	return def
}

func intEnv(values map[string]string, key string, def int) (int, error) {
	v, ok := values[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not a number", key, v)
	}
	return n, nil
}

func known() string {
	return strings.Join(envKeys, ", ")
}
