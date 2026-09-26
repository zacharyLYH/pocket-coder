package httpapi

import "testing"

func TestNpmPackage(t *testing.T) {
	cases := map[string]string{
		"npm i -g opencode-ai":         "opencode-ai",
		"npm install -g @openai/codex": "@openai/codex",
		"npm install -g --ignore-scripts @earendil-works/pi-coding-agent": "@earendil-works/pi-coding-agent",
		"npm i -g freebuff && freebuff --version || true":                 "freebuff",
		"curl -fsSL https://cli.kiro.dev/install | bash":                  "",
		"":                       "",
		"vi notes.txt":           "",
		"pip install aider-chat": "",
	}
	for in, want := range cases {
		if got := npmPackage(in); got != want {
			t.Errorf("npmPackage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormVersion(t *testing.T) {
	cases := map[string]string{
		"1.2.3":             "1.2.3",
		"v1.2.3":            "1.2.3",
		"claude 1.2.3":      "1.2.3",
		"opencode-ai 0.9.0": "0.9.0",
		"":                  "",
	}
	for in, want := range cases {
		if got := normVersion(in); got != want {
			t.Errorf("normVersion(%q) = %q, want %q", in, got, want)
		}
	}
}
