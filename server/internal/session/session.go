// Package session manages tmux sessions inside a project's container:
// plain-shell sessions, and harness launches — install-on-demand, CLI
// validation, and the `|| echo` failure story (PRD §5).
package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sps/internal/docker"
	"sps/internal/harness"
	"sps/internal/textutil"
)

// ErrInvalidName means a session name outside the allowed shape.
var ErrInvalidName = errors.New("invalid session name")

// ErrNotCLI is the PRD §5 rejection, verbatim: the probe hung, crashed, or
// printed nothing, so the "command" is not a terminal program.
var ErrNotCLI = errors.New("not a CLI — it looks like it wants a display (an IDE/GUI), and the platform only runs terminal programs.")

// ErrNotInstalled means the harness binary is absent and installs are never
// implicit: the user installs per project from the home page.
var ErrNotInstalled = errors.New("not installed in this project — install it from the home page first")

// ErrDuplicate means a session with that name already exists in tmux.
var ErrDuplicate = errors.New("duplicate session")

const (
	installTimeout  = 2 * time.Minute
	validateTimeout = 15 * time.Second // CLIs like freebuff download a platform binary on first run
	repoDir         = "/workspace/repo"
	fallbackRepoDir = "/workspace"
)

// nameRe keeps names boring so they are safe as exec args, URL path
// segments, and event payloads.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ValidName reports whether name may be used as a tmux session name.
func ValidName(name string) bool { return nameRe.MatchString(name) }

// splitSuffix splits "base-<n>" into its base and numeric suffix. The scan
// runs backward so "a-2-3" splits as ("a-2", 3), never ("a", 2). ok is false
// when no dash-digit tail exists.
func splitSuffix(name string) (base string, n int, ok bool) {
	for i := len(name) - 1; i > 0; i-- {
		if name[i] == '-' {
			if v, err := strconv.Atoi(name[i+1:]); err == nil {
				return name[:i], v, true
			}
		}
	}
	return name, 0, false
}

// HarnessSuffixed reports whether name is exactly base+"-"+<number> — the
// namespace Launch uses for harness sessions. A bare name ("opencode") is
// NOT suffixed, so a plain shell that happens to share a harness's id is
// never mistaken for a harness session.
func HarnessSuffixed(name, base string) bool {
	b, _, ok := splitSuffix(name)
	return ok && b == base
}

// ThemeArgs are the tmux commands appended to every session create so the
// container's tmux matches the web terminal (TERM_THEME in
// web/src/components/terminal/TerminalPane.tsx): truecolor passthrough, COLORTERM for
// pane processes, and a status bar in the app palette instead of tmux's
// default green. All global options, so applying them on every create is
// idempotent and upgrades existing tmux servers too.
func ThemeArgs() []string {
	return []string{
		// Advertise RGB to the pane side (and the attach client, which runs
		// with the TERM env set in Attach) so TUIs emit truecolor instead of
		// a 256-color approximation.
		";", "set-option", "-sa", "terminal-features", ",*:RGB",
		";", "set-environment", "-g", "COLORTERM", "truecolor",
		// Status bar in the app palette: accent session name, dim chrome.
		";", "set-option", "-g", "status-style", "bg=#0a0e14,fg=#b3b1ad",
		";", "set-option", "-g", "status-left", "#[fg=#e6b450,bold]#S #[fg=#686868]",
		";", "set-option", "-g", "status-right", "#[fg=#686868]#h #[fg=#b3b1ad]%H:%M",
		";", "set-option", "-g", "window-status-style", "fg=#686868,bg=#0a0e14",
		";", "set-option", "-g", "window-status-current-style", "fg=#e6b450,bg=#0a0e14",
	}
}

// Entry is one tmux session.
type Entry struct {
	Name string `json:"name"`
}

// Service runs tmux in a project container via Docker exec.
type Service struct {
	dkr docker.Client
}

// New wires the service to a Docker client.
func New(dkr docker.Client) *Service { return &Service{dkr: dkr} }

// List returns the sessions from `tmux list-sessions`. No server running
// (a fresh container) is an empty list, not an error.
func (s *Service) List(ctx context.Context, container string) ([]Entry, error) {
	res, err := s.dkr.Exec(ctx, container, []string{"tmux", "list-sessions", "-F", "#{session_name}"}, false)
	if err != nil {
		return nil, err
	}
	// A container that never ran tmux has no server at all; different tmux
	// builds word it differently ("no server running", "error connecting
	// to ..."). Both mean zero sessions, not failure.
	if res.ExitCode != 0 &&
		(strings.Contains(res.Output, "no server running") || strings.Contains(res.Output, "error connecting")) {
		return []Entry{}, nil
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("list sessions: %s", strings.TrimSpace(res.Output))
	}
	var out []Entry
	for _, line := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, Entry{Name: line})
		}
	}
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

// Exists reports whether the named session exists (`tmux has-session`,
// exit 1 = no).
func (s *Service) Exists(ctx context.Context, container, name string) (bool, error) {
	res, err := s.dkr.Exec(ctx, container, []string{"tmux", "has-session", "-t", name}, false)
	if err != nil {
		return false, err
	}
	switch res.ExitCode {
	case 0:
		return true, nil
	case 1:
		return false, nil
	default:
		return false, fmt.Errorf("has-session %s: %s", name, strings.TrimSpace(res.Output))
	}
}

// newSessionArgs builds the tmux create argv shared by plain shells and
// harness launches: escape-time 0 (see Create) plus the theme options.
func newSessionArgs(name, dir string, cmd []string) []string {
	args := append([]string{"tmux", "new-session", "-d", "-s", name, "-c", dir}, cmd...)
	args = append(args, ";", "set-option", "-s", "escape-time", "0")
	return append(args, ThemeArgs()...)
}

// Create starts a detached session running a plain shell in /workspace.
// A duplicate name fails with the engine's message surfaced.
//
// Every session zeroes tmux's escape-time (default 500ms): with a real
// keyboard over the bridge, a lone ESC followed by any key within that
// window is parsed as Meta-key and silently swallowed — unacceptable for
// the vim-style CLIs this platform exists to run.
func (s *Service) Create(ctx context.Context, container, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	res, err := s.dkr.Exec(ctx, container, newSessionArgs(name, "/workspace", nil), false)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		if strings.Contains(res.Output, "duplicate session") {
			return fmt.Errorf("%w: %s", ErrDuplicate, name)
		}
		return fmt.Errorf("create session %s: %s", name, strings.TrimSpace(res.Output))
	}
	return nil
}

// Attach runs `tmux attach -t name` with a TTY wired through stdin/stdout/
// stderr. It returns immediately with the exec id (needed for Resize); the
// outcome arrives on done when the client detaches or the session ends.
//
// The env wrapper matters: docker exec does not set TERM, and with TERM
// empty the tmux client assumes a dumb outer terminal and downgrades every
// color the TUIs this platform exists to run — the classic "colors look
// almost right but washed out" failure.
func (s *Service) Attach(ctx context.Context, container, name string, stdin io.Reader, stdout, stderr io.Writer) (execID string, done <-chan docker.ExecDone, err error) {
	// Apply the theme on attach as well as create. Sessions survive browser
	// disconnects and may have been created before the current theme was
	// introduced; without this, existing OpenCode sessions keep tmux's
	// default status colors and can negotiate only the wrong color depth.
	cmd := []string{
		"env", "TERM=xterm-256color", "COLORTERM=truecolor",
		"tmux",
	}
	cmd = append(cmd, ThemeArgs()...)
	cmd = append(cmd, ";", "attach", "-t", name)
	return s.dkr.Attach(ctx, container, cmd, stdin, stdout, stderr, true)
}

// Resize resizes the TTY of a running attach.
func (s *Service) Resize(ctx context.Context, execID string, rows, cols int) error {
	return s.dkr.ResizeTTY(ctx, execID, rows, cols)
}

// ParseBase strips a trailing -<n> numeric suffix from a session name and
// returns the base: a harness session "opencode-3" → "opencode", a plain
// shell "dev" → "dev". The suffix is the convention Launch uses; the base
// alone tells restart which plugin to relaunch.
func ParseBase(name string) string {
	base, _, ok := splitSuffix(name)
	if !ok {
		return name
	}
	return base
}

// nextName returns <id>-<n> using the LOWEST free suffix for the prefix, so
// numbers stay small after kills. Sessions are discovered from tmux, never
// stored. Not atomic against concurrent launches: tmux rejects the duplicate
// and the caller surfaces the error.
func (s *Service) nextName(ctx context.Context, container, id string) (string, error) {
	existing, err := s.List(ctx, container)
	if err != nil {
		return "", err
	}
	taken := map[int]bool{}
	for _, e := range existing {
		if base, n, ok := splitSuffix(e.Name); ok && base == id {
			taken[n] = true
		}
	}
	for n := 1; ; n++ {
		if !taken[n] {
			return fmt.Sprintf("%s-%d", id, n), nil
		}
	}
}

// Installed probes which harness commands exist in the container, in one
// round trip. Returns id->installed for the harnesses the caller passes.
func (s *Service) Installed(ctx context.Context, container string, cmds []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(cmds) == 0 {
		return out, nil
	}
	script := `for c in ` + strings.Join(cmds, " ") + `; do command -v "$c" >/dev/null && echo "$c"; done`
	res, err := s.dkr.Exec(ctx, container, []string{"bash", "-lc", script}, false)
	if err != nil {
		return nil, err
	}
	for _, c := range cmds {
		out[c] = false
	}
	for _, line := range strings.Split(strings.TrimSpace(res.Output), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out[line] = true
		}
	}
	return out, nil
}

// repoTarget picks /workspace/repo when a clone lives there, else /workspace
// for blank sandboxes.
func (s *Service) repoTarget(ctx context.Context, container string) (string, error) {
	res, err := s.dkr.Exec(ctx, container, []string{"test", "-d", repoDir + "/.git"}, false)
	if err != nil {
		return "", err
	}
	if res.ExitCode == 0 {
		return repoDir, nil
	}
	return fallbackRepoDir, nil
}

// commandPath reports whether the harness binary exists in the container.
func (s *Service) commandPath(ctx context.Context, container, cmd string) (bool, error) {
	res, err := s.dkr.Exec(ctx, container, []string{"bash", "-lc", "command -v " + cmd}, false)
	if err != nil {
		return false, err
	}
	return res.ExitCode == 0, nil
}

// Launch runs the full PRD §5 flow for a harness plugin under the next free
// <harnessID>-<n> name; see LaunchNamed for the pipeline.
func (s *Service) Launch(ctx context.Context, container string, h harness.Harness) (string, error) {
	name, err := s.nextName(ctx, container, h.ID)
	if err != nil {
		return "", err
	}
	return s.LaunchNamed(ctx, container, name, h)
}

// ensureInstalled makes the harness binary present in the container: a
// missing binary runs the plugin's install command (bounded by
// installTimeout) and is then re-checked. A plugin with no install command
// is tolerated here — the launch pipeline surfaces the missing command
// in-session via the `|| echo` failure story.
func (s *Service) ensureInstalled(ctx context.Context, container string, h harness.Harness) error {
	found, err := s.commandPath(ctx, container, harness.Binary(h))
	if err != nil {
		return err
	}
	if found || h.Install == "" {
		return nil
	}
	return s.runInstall(ctx, container, h)
}

// runInstall executes the plugin's install command and re-checks the binary.
func (s *Service) runInstall(ctx context.Context, container string, h harness.Harness) error {
	installCtx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	res, ierr := s.dkr.Exec(installCtx, container, []string{"bash", "-lc", h.Install}, false)
	if ierr != nil {
		return fmt.Errorf("install %q: %v", h.ID, ierr)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("install %q failed: %s", h.ID, textutil.Tail(res.Output))
	}
	if found, ferr := s.commandPath(ctx, container, harness.Binary(h)); ferr != nil || !found {
		return fmt.Errorf("install %q ran but %q is still missing", h.ID, h.Command)
	}
	return nil
}

// execTimeout bounds one arbitrary command in one container. Installs and
// upgrades (npm/pip) are slow; ten minutes is generous without hanging the
// synchronous home-page flow forever.
const execTimeout = 10 * time.Minute

// ExecCommand runs an arbitrary shell command in a container synchronously
// (the home-page "run in projects" box: installs, upgrades, maintenance).
// Output is trimmed; a non-zero exit is an error carrying the output tail.
func (s *Service) ExecCommand(ctx context.Context, container, command string) (string, error) {
	execCtx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	res, err := s.dkr.Exec(execCtx, container, []string{"bash", "-lc", command}, false)
	if err != nil {
		return "", err
	}
	out := strings.TrimSpace(res.Output)
	if res.ExitCode != 0 {
		return out, fmt.Errorf("exit %d: %s", res.ExitCode, textutil.Tail(out))
	}
	return out, nil
}

// InstallHarness is the explicit install: the binary must end up present AND
// CLI-valid, and a plugin with no install command is a hard error — the home
// page surfaces exactly this, so "installed" always means usable.
func (s *Service) InstallHarness(ctx context.Context, container string, h harness.Harness) error {
	found, err := s.commandPath(ctx, container, harness.Binary(h))
	if err != nil {
		return err
	}
	if !found {
		if h.Install == "" {
			return fmt.Errorf("%q is not installed and the plugin has no install command", h.Command)
		}
		if err := s.runInstall(ctx, container, h); err != nil {
			return err
		}
	}
	return s.validateCLI(ctx, container, harness.Binary(h))
}

// validateCLI runs `<binary> --version` (or --help) inside a forced TTY with
// a few-second timeout. A real CLI prints something and exits; anything else
// — hang, display error, silence — is rejected with ErrNotCLI. Only the
// binary is probed, never the full command: `vi hello.txt --version` would
// open an editor instead of answering.
func (s *Service) validateCLI(ctx context.Context, container, cmd string) error {
	vCtx, cancel := context.WithTimeout(ctx, validateTimeout)
	defer cancel()
	res, err := s.dkr.Exec(vCtx, container, []string{
		"bash", "-lc", fmt.Sprintf("%s --version || %s --help", cmd, cmd),
	}, true)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(vCtx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w (%q did not exit within %s)", ErrNotCLI, cmd, validateTimeout)
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Output) == "" {
		return fmt.Errorf("%w (%q printed nothing and exited %d)", ErrNotCLI, cmd, res.ExitCode)
	}
	return nil
}

// Kill terminates a session by name. Killing an already-gone session is not
// an error (idempotent delete).
func (s *Service) Kill(ctx context.Context, container, name string) error {
	if !ValidName(name) {
		return fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	res, err := s.dkr.Exec(ctx, container, []string{"tmux", "kill-session", "-t", name}, false)
	if err != nil {
		return err
	}
	if res.ExitCode != 0 && !strings.Contains(res.Output, "can't find session") {
		return fmt.Errorf("kill session %s: %s", name, strings.TrimSpace(res.Output))
	}
	return nil
}

// LaunchNamed is the launch pipeline: the harness must ALREADY be installed
// in this project (installs are explicit, home-page, per project — never a
// hidden download at launch), CLI validation (forced TTY, few-second
// timeout), then a detached tmux session whose command ends with the
// `|| echo` failure story. Restart uses it with an explicit name so the
// suffix does not drift.
func (s *Service) LaunchNamed(ctx context.Context, container, name string, h harness.Harness) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	dir, err := s.repoTarget(ctx, container)
	if err != nil {
		return "", err
	}
	if found, err := s.commandPath(ctx, container, harness.Binary(h)); err != nil {
		return "", err
	} else if !found {
		return "", fmt.Errorf("%w (%q)", ErrNotInstalled, h.Command)
	}
	// Native config lands before validation: the probe may refuse a CLI
	// that would run fine with its config present (e.g. one that exits
	// when it finds no key), and the launched command must see it either way.
	if h.ConfigPath != "" && len(h.Config) > 0 {
		cfgDir := filepath.Dir(h.ConfigPath)
		if res, err := s.dkr.Exec(ctx, container, []string{"mkdir", "-p", cfgDir}, false); err != nil {
			return "", fmt.Errorf("write config for %q: %w", h.ID, err)
		} else if res.ExitCode != 0 {
			return "", fmt.Errorf("write config for %q: mkdir %s: %s", h.ID, cfgDir, strings.TrimSpace(res.Output))
		}
		if err := s.dkr.WriteFile(ctx, container, h.ConfigPath, h.Config); err != nil {
			return "", fmt.Errorf("write config for %q: %w", h.ID, err)
		}
	}
	if err := s.validateCLI(ctx, container, harness.Binary(h)); err != nil {
		return "", err
	}
	shellLine := fmt.Sprintf(`%s || echo "[%s exited: $?]"`, h.Command, h.Command)
	res, err := s.dkr.Exec(ctx, container, newSessionArgs(name, dir,
		[]string{"bash", "-lc", shellLine, ";", "set-option", "remain-on-exit", "on"}), false)
	if err != nil {
		return "", err
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("launch %s: %s", name, strings.TrimSpace(res.Output))
	}
	return name, nil
}
