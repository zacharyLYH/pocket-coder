// Server deploy-key endpoints: show the public half, probe GitHub auth,
// and regenerate. The keypair itself is generated on first boot
// (sshkeys.EnsureKeypair); these endpoints only read, test, or replace
// it. The private half never renders.
package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"pcoder/internal/obs"
	"pcoder/internal/sshkeys"
)

func serverKeypair(d Deps, w http.ResponseWriter) (sshkeys.Key, bool) {
	if d.SSHKeys == nil {
		writeInternalErr(w, "ssh store", errors.New("no ssh store"))
		return sshkeys.Key{}, false
	}
	kp, ok := d.SSHKeys.Get()
	if !ok {
		writeErr(w, http.StatusConflict, "server key not generated yet — restart the server")
		return sshkeys.Key{}, false
	}
	return kp, true
}

func handleServerKey(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		kp, ok := serverKeypair(d, w)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"publicKey": kp.PublicKey, "fingerprint": kp.Fingerprint, "createdAt": kp.CreatedAt,
		})
	}
}

// sshAuthUser matches GitHub's success line: "Hi octocat! You've
// successfully authenticated, but GitHub does not provide shell access."
var sshAuthUser = regexp.MustCompile(`(?m)^Hi (.+?)!`)

// parseProbeResult reads GitHub's answer: exit codes lie (1 on success),
// the output does not. Returns the authed username.
func parseProbeResult(text string) (user string, ok bool) {
	if !strings.Contains(text, "successfully authenticated") {
		return "", false
	}
	if m := sshAuthUser.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	return "", true
}

// probeGitHubSSH runs `ssh -T git@github.com` with the stored key in an
// isolated HOME (accept-new answers the known-hosts prompt). GitHub exits
// 1 on success, so the output — not the code — is the verdict.
// A var so handler tests can stub the network hop.
var probeGitHubSSH = func(ctx context.Context, kp sshkeys.Key) (string, error) {
	if _, err := exec.LookPath("ssh"); err != nil {
		return "", errors.New("ssh client unavailable on the server")
	}
	dir, err := os.MkdirTemp("", "pcoder-sshprobe")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "id_ed25519"), []byte(kp.PrivateKey), 0o600); err != nil {
		return "", err
	}
	cfg := "Host *\n  StrictHostKeyChecking accept-new\n  IdentityFile " + filepath.Join(dir, "id_ed25519") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(cfg), 0o600); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// IdentitiesOnly + a cleared agent socket pin the handshake to our
	// key: without them a stray agent key could authenticate as someone
	// else and the probe would report the wrong username.
	cmd := exec.CommandContext(ctx, "ssh", "-T",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		"-o", "IdentitiesOnly=yes",
		"-F", filepath.Join(dir, "config"),
		"git@github.com")
	env := os.Environ()
	clean := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if !strings.HasPrefix(kv, "SSH_AUTH_SOCK=") && !strings.HasPrefix(kv, "HOME=") {
			clean = append(clean, kv)
		}
	}
	cmd.Env = append(clean, "HOME="+dir)
	out, _ := cmd.CombinedOutput()
	text := string(out)
	if user, ok := parseProbeResult(text); ok {
		return user, nil
	}
	if ctx.Err() != nil {
		return "", errors.New("ssh probe timed out — check server egress to github.com:22")
	}
	return "", fmt.Errorf("GitHub rejected the key — add the public half to GitHub first: %s", firstLine(text))
}

func handleServerKeyTest(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SSHKeyTest, "ssh probe failed", err, nil)
			}
		}()
		kp, ok := serverKeypair(d, w)
		if !ok {
			return
		}
		user, perr := probeGitHubSSH(r.Context(), kp)
		if perr != nil {
			err = perr
			writeErr(w, http.StatusBadGateway, err.Error())
			return
		}
		obs.Info(r.Context(), obs.SSHKeyTest, "ssh probe ok", map[string]any{"user": user})
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": user})
	}
}

func handleServerKeyRegen(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var err error
		defer func() {
			if err != nil {
				obsFail(r, obs.SSHKeyRegen, "ssh regen failed", err, nil)
			}
		}()
		if d.SSHKeys == nil {
			writeInternalErr(w, "ssh store", errors.New("no ssh store"))
			return
		}
		kp, rerr := d.SSHKeys.Regenerate()
		if rerr != nil {
			err = rerr
			writeInternalErr(w, "regenerate key", rerr)
			return
		}
		obs.Info(r.Context(), obs.SSHKeyRegen, "server key regenerated", map[string]any{"fingerprint": kp.Fingerprint})
		writeJSON(w, http.StatusOK, map[string]any{
			"publicKey": kp.PublicKey, "fingerprint": kp.Fingerprint, "createdAt": kp.CreatedAt,
		})
	}
}
