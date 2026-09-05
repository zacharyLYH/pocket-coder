package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/mock"

	"sps/internal/docker"
	"sps/internal/harness"
	dockermocks "sps/mocks/docker"
)

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"main":                  true,
		"a":                     true,
		"A-b_9":                 true,
		"":                      false,
		"-lead":                 false,
		"has space":             false,
		"sl/ash":                false,
		"unicode-é":             false,
		strings.Repeat("x", 64): true,
		strings.Repeat("x", 65): false,
	}
	for name, want := range cases {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestList(t *testing.T) {
	listCmd := []string{"tmux", "list-sessions", "-F", "#{session_name}"}

	t.Run("returns names in order", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", listCmd, false).
			Return(docker.ExecResult{ExitCode: 0, Output: "main\nwork\n"}, nil)
		got, err := New(d).List(context.Background(), "c1")
		if err != nil || len(got) != 2 || got[0].Name != "main" || got[1].Name != "work" {
			t.Fatalf("got %+v err=%v", got, err)
		}
	})

	t.Run("no tmux server is an empty list, not an error (both wordings)", func(t *testing.T) {
		for _, out := range []string{
			"no server running on /tmp/tmux-0/default",
			"error connecting to /tmp/tmux-0/default (No such file or directory)",
		} {
			d := dockermocks.NewMockClient(t)
			d.EXPECT().Exec(mock.Anything, "c1", listCmd, false).
				Return(docker.ExecResult{ExitCode: 1, Output: out}, nil)
			got, err := New(d).List(context.Background(), "c1")
			if err != nil || len(got) != 0 {
				t.Fatalf("%q: got %+v err=%v, want empty list", out, got, err)
			}
		}
	})

	t.Run("other nonzero exit surfaces the output", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", listCmd, false).
			Return(docker.ExecResult{ExitCode: 70, Output: "tmux: exploded"}, nil)
		if _, err := New(d).List(context.Background(), "c1"); err == nil || !strings.Contains(err.Error(), "exploded") {
			t.Fatalf("err = %v, want the tmux output", err)
		}
	})

	t.Run("engine error propagates", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", listCmd, false).
			Return(docker.ExecResult{}, errors.New("engine on fire"))
		if _, err := New(d).List(context.Background(), "c1"); err == nil {
			t.Fatal("expected engine error")
		}
	})
}

func TestExists(t *testing.T) {
	hasCmd := []string{"tmux", "has-session", "-t", "main"}

	t.Run("exit 0 means exists", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", hasCmd, false).Return(docker.ExecResult{ExitCode: 0}, nil)
		got, err := New(d).Exists(context.Background(), "c1", "main")
		if err != nil || !got {
			t.Fatalf("got %v err=%v, want true", got, err)
		}
	})

	t.Run("exit 1 means missing", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", hasCmd, false).Return(docker.ExecResult{ExitCode: 1}, nil)
		got, err := New(d).Exists(context.Background(), "c1", "main")
		if err != nil || got {
			t.Fatalf("got %v err=%v, want false", got, err)
		}
	})

	t.Run("other exits are engine failures", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", hasCmd, false).Return(docker.ExecResult{ExitCode: 2, Output: "boom"}, nil)
		if _, err := New(d).Exists(context.Background(), "c1", "main"); err == nil {
			t.Fatal("expected error for exit 2")
		}
	})
}

func TestCreate(t *testing.T) {
	t.Run("rejects invalid names without touching docker", func(t *testing.T) {
		d := dockermocks.NewMockClient(t) // no expectations: any Exec call panics the mock
		if err := New(d).Create(context.Background(), "c1", "-evil"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("err = %v, want ErrInvalidName", err)
		}
	})

	t.Run("starts a detached shell in /workspace", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1",
			append([]string{"tmux", "new-session", "-d", "-s", "work", "-c", "/workspace",
				";", "set-option", "-s", "escape-time", "0"}, ThemeArgs()...), false).
			Return(docker.ExecResult{ExitCode: 0}, nil)
		if err := New(d).Create(context.Background(), "c1", "work"); err != nil {
			t.Fatalf("create: %v", err)
		}
	})

	t.Run("duplicate name surfaces the tmux message", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, false).
			Return(docker.ExecResult{ExitCode: 1, Output: "duplicate session: work"}, nil)
		err := New(d).Create(context.Background(), "c1", "work")
		if err == nil || !strings.Contains(err.Error(), "duplicate session") {
			t.Fatalf("err = %v, want duplicate-session detail", err)
		}
	})
}

// TestAttachWiring pins the argv and TTY mode of the attach exec — the
// contract the browser transport depends on.
func TestAttachWiring(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	done := make(chan docker.ExecDone, 1)
	done <- docker.ExecDone{ExitCode: 7}
	wantAttach := append([]string{"env", "TERM=xterm-256color", "COLORTERM=truecolor", "tmux"}, ThemeArgs()...)
	wantAttach = append(wantAttach, ";", "attach", "-t", "main")
	d.EXPECT().Attach(mock.Anything, "c1",
		wantAttach,
		mock.Anything, mock.Anything, mock.Anything, true).
		Return("exec-9", done, nil)

	var stdin strings.Reader
	var stdout, stderr strings.Builder
	eid, gotDone, err := New(d).Attach(context.Background(), "c1", "main", &stdin, &stdout, &stderr)
	if err != nil || eid != "exec-9" {
		t.Fatalf("got (%q, %v), want exec-9 passthrough", eid, err)
	}
	if out := <-gotDone; out.ExitCode != 7 {
		t.Fatalf("done = %+v, want exit code 7", out)
	}

	d.EXPECT().ResizeTTY(mock.Anything, "exec-9", 40, 120).Return(nil)
	if err := New(d).Resize(context.Background(), "exec-9", 40, 120); err != nil {
		t.Fatalf("resize: %v", err)
	}
}

func newHarness() harness.Harness {
	return harness.Harness{ID: "fake", Name: "Fake", Command: "fakecli"}
}

// TestInstallHarness covers the explicit (home-page) install: present
// binaries are only validated, missing ones run the install command first,
// failures and install-less plugins are hard errors with usable messages.
func TestInstallHarness(t *testing.T) {
	lookup := []string{"bash", "-lc", "command -v fakecli"}
	validate := []string{"bash", "-lc", "fakecli --version || fakecli --help"}

	t.Run("already installed only validates", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", lookup, false).
			Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli"}, nil)
		d.EXPECT().Exec(mock.Anything, "c1", validate, true).
			Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)
		if err := New(d).InstallHarness(context.Background(), "c1", newHarness()); err != nil {
			t.Fatalf("InstallHarness: %v", err)
		}
	})

	t.Run("missing binary installs then validates", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		h := newHarness()
		h.Install = "npm i -g fakecli"
		install := []string{"bash", "-lc", h.Install}
		// misses: InstallHarness's initial probe; the post-install re-check hits
		lookups := 0
		d.EXPECT().Exec(mock.Anything, "c1", lookup, false).
			RunAndReturn(func(context.Context, string, []string, bool) (docker.ExecResult, error) {
				lookups++
				if lookups < 2 {
					return docker.ExecResult{ExitCode: 1}, nil
				}
				return docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli"}, nil
			})
		d.EXPECT().Exec(mock.Anything, "c1", install, false).
			Return(docker.ExecResult{ExitCode: 0, Output: "added 1 package\n"}, nil)
		d.EXPECT().Exec(mock.Anything, "c1", validate, true).
			Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)
		if err := New(d).InstallHarness(context.Background(), "c1", h); err != nil {
			t.Fatalf("InstallHarness: %v", err)
		}
	})

	t.Run("failing install surfaces the tail", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		h := newHarness()
		h.Install = "npm i -g fakecli"
		d.EXPECT().Exec(mock.Anything, "c1", lookup, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		d.EXPECT().Exec(mock.Anything, "c1", []string{"bash", "-lc", h.Install}, false).
			Return(docker.ExecResult{ExitCode: 1, Output: "npm ERR! network unreachable"}, nil)
		err := New(d).InstallHarness(context.Background(), "c1", h)
		if err == nil || !strings.Contains(err.Error(), "npm ERR! network unreachable") {
			t.Fatalf("err = %v, want install output surfaced", err)
		}
	})

	t.Run("missing binary with no install command is a clear error", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", lookup, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		err := New(d).InstallHarness(context.Background(), "c1", newHarness())
		if err == nil || !strings.Contains(err.Error(), "no install command") {
			t.Fatalf("err = %v, want 'no install command'", err)
		}
	})
}

// TestLaunchHappyPath walks the full pipeline with every exec pinned: probe
// repo dir, command lookup, CLI validation (forced TTY), and the new-session
// argv carrying the `|| echo` failure story.
func TestLaunchHappyPath(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	ctx := context.Background()

	listCmd := []string{"tmux", "list-sessions", "-F", "#{session_name}"}
	d.EXPECT().Exec(mock.Anything, "c1", listCmd, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\n"}, nil)
	d.EXPECT().Exec(mock.Anything, "c1", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil) // blank project → /workspace
	d.EXPECT().Exec(mock.Anything, "c1", []string{"bash", "-lc", "command -v fakecli"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/local/bin/fakecli"}, nil)
	validateCmd := []string{"bash", "-lc", "fakecli --version || fakecli --help"}
	d.EXPECT().Exec(mock.Anything, "c1", validateCmd, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "fakecli 1.0\n"}, nil)
	wantSession := append([]string{"tmux", "new-session", "-d", "-s", "fake-1", "-c", "/workspace",
		"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
		";", "set-option", "remain-on-exit", "on",
		";", "set-option", "-s", "escape-time", "0"}, ThemeArgs()...)
	d.EXPECT().Exec(mock.Anything, "c1", wantSession, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	name, err := New(d).Launch(ctx, "c1", newHarness())
	if err != nil || name != "fake-1" {
		t.Fatalf("got (%q, %v), want fake-1", name, err)
	}
}

// TestLaunchWritesConfigBeforeValidation pins the v2 ordering: the plugin's
// native config is written into the container BEFORE the CLI probe runs —
// a probe may reject a CLI that would behave with its config present.
func TestLaunchWritesConfigBeforeValidation(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	ctx := context.Background()

	h := newHarness()
	h.ConfigPath = "/root/.fakecli/config.json"
	h.Config = []byte(`{"model":"test"}`)

	var order []string
	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, cmd []string, tty bool) (docker.ExecResult, error) {
			switch {
			case len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "list-sessions":
				return docker.ExecResult{ExitCode: 0}, nil
			case len(cmd) >= 1 && cmd[0] == "test":
				return docker.ExecResult{ExitCode: 1}, nil
			case len(cmd) == 3 && cmd[2] == "command -v fakecli":
				order = append(order, "lookup")
				return docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli"}, nil
			case tty:
				// the config file must already exist when validation runs
				order = append(order, "validate")
				return docker.ExecResult{ExitCode: 0, Output: "1.0\n"}, nil
			case len(cmd) >= 2 && cmd[0] == "tmux":
				return docker.ExecResult{ExitCode: 0}, nil
			default:
				return docker.ExecResult{}, nil
			}
		})
	d.EXPECT().WriteFile(mock.Anything, "c1", h.ConfigPath, mock.Anything).
		RunAndReturn(func(_ context.Context, _ string, _ string, content []byte) error {
			if string(content) != string(h.Config) {
				t.Fatalf("config content = %s, want %s", content, h.Config)
			}
			order = append(order, "write-config")
			return nil
		})

	if _, err := New(d).Launch(ctx, "c1", h); err != nil {
		t.Fatalf("launch with config: %v", err)
	}
	want := []string{"lookup", "write-config", "validate"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v (config written before validate)", order, want)
	}
}

func TestLaunchNumberingAndRepoDir(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	ctx := context.Background()

	d.EXPECT().Exec(mock.Anything, "c1",
		[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "main\nfake-2\nfake-9x\n"}, nil)
	// fake-2 is taken; lowest-free-number picks fake-1 (9x is not a number)
	// cloned repo present → sessions start in /workspace/repo
	d.EXPECT().Exec(mock.Anything, "c1", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 0}, nil)
	d.EXPECT().Exec(mock.Anything, "c1", []string{"bash", "-lc", "command -v fakecli"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli\n"}, nil)
	d.EXPECT().Exec(mock.Anything, "c1",
		[]string{"bash", "-lc", "fakecli --version || fakecli --help"}, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "1.0\n"}, nil)
	d.EXPECT().Exec(mock.Anything, "c1",
		append([]string{"tmux", "new-session", "-d", "-s", "fake-1", "-c", "/workspace/repo",
			"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
			";", "set-option", "remain-on-exit", "on",
			";", "set-option", "-s", "escape-time", "0"}, ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	name, err := New(d).Launch(ctx, "c1", newHarness())
	if err != nil || name != "fake-1" {
		t.Fatalf("got (%q, %v), want fake-1 (lowest free suffix)", name, err)
	}
}

// TestLaunchRequiresInstalledHarness pins the explicit-install model: a
// launch never downloads anything. A missing binary is a hard 422-mapped
// error pointing at the home page, even when the plugin has an install
// command — installs happen only via InstallHarness, per project, on demand
// from the user.
func TestLaunchRequiresInstalledHarness(t *testing.T) {
	t.Run("missing binary is refused without installing", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		ctx := context.Background()
		h := newHarness()
		h.Install = "npm i -g fakecli"

		var ranInstall bool
		d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, mock.Anything).
			RunAndReturn(func(_ context.Context, _ string, cmd []string, _ bool) (docker.ExecResult, error) {
				switch {
				case len(cmd) >= 2 && cmd[0] == "tmux" && cmd[1] == "list-sessions":
					return docker.ExecResult{ExitCode: 0}, nil
				case len(cmd) >= 1 && cmd[0] == "test":
					return docker.ExecResult{ExitCode: 1}, nil // blank project
				case len(cmd) == 3 && cmd[2] == "command -v fakecli":
					return docker.ExecResult{ExitCode: 1}, nil // missing
				default:
					if len(cmd) >= 2 && cmd[0] == "bash" && cmd[1] == "-lc" && cmd[2] == h.Install {
						ranInstall = true
					}
					return docker.ExecResult{}, nil
				}
			})

		_, err := New(d).Launch(ctx, "c1", h)
		if !errors.Is(err, ErrNotInstalled) {
			t.Fatalf("err = %v, want ErrNotInstalled", err)
		}
		if ranInstall {
			t.Fatal("launch ran the install command — installs must be explicit")
		}
	})

	t.Run("present binary launches without touching the installer", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		ctx := context.Background()

		d.EXPECT().Exec(mock.Anything, "c1",
			[]string{"tmux", "list-sessions", "-F", "#{session_name}"}, false).
			Return(docker.ExecResult{ExitCode: 0}, nil)
		d.EXPECT().Exec(mock.Anything, "c1", []string{"test", "-d", "/workspace/repo/.git"}, false).
			Return(docker.ExecResult{ExitCode: 1}, nil)
		d.EXPECT().Exec(mock.Anything, "c1", []string{"bash", "-lc", "command -v fakecli"}, false).
			Return(docker.ExecResult{ExitCode: 0, Output: "/usr/bin/fakecli"}, nil)
		d.EXPECT().Exec(mock.Anything, "c1",
			[]string{"bash", "-lc", "fakecli --version || fakecli --help"}, true).
			Return(docker.ExecResult{ExitCode: 0, Output: "1.0\n"}, nil)
		d.EXPECT().Exec(mock.Anything, "c1",
			append([]string{"tmux", "new-session", "-d", "-s", "fake-1", "-c", "/workspace",
				"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
				";", "set-option", "remain-on-exit", "on",
				";", "set-option", "-s", "escape-time", "0"}, ThemeArgs()...), false).
			Return(docker.ExecResult{ExitCode: 0}, nil)

		if _, err := New(d).Launch(ctx, "c1", newHarness()); err != nil {
			t.Fatalf("launch with installed harness: %v", err)
		}
	})
}

func TestLaunchValidationHangIsNotCLI(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	svc := New(d)
	ctx := context.Background()

	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, false).
		Return(docker.ExecResult{}, nil).Times(3) // list + probe + lookup(missing→no install? no: found)
	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, true).
		RunAndReturn(func(vctx context.Context, _ string, _ []string, _ bool) (docker.ExecResult, error) {
			<-vctx.Done() // a GUI program that never exits
			return docker.ExecResult{}, vctx.Err()
		})

	_, err := svc.Launch(ctx, "c1", newHarness())
	if !errors.Is(err, ErrNotCLI) || !strings.Contains(err.Error(), "did not exit") {
		t.Fatalf("err = %v, want ErrNotCLI for a hanging command", err)
	}
}

func TestLaunchValidationSilentIsNotCLI(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	ctx := context.Background()

	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, false).
		Return(docker.ExecResult{}, nil).Times(3)
	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, true).
		Return(docker.ExecResult{ExitCode: 0, Output: ""}, nil) // prints nothing

	_, err := New(d).Launch(ctx, "c1", newHarness())
	if !errors.Is(err, ErrNotCLI) {
		t.Fatalf("err = %v, want ErrNotCLI for silent success", err)
	}
}

func TestKill(t *testing.T) {
	t.Run("idempotent when already gone", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", []string{"tmux", "kill-session", "-t", "main"}, false).
			Return(docker.ExecResult{ExitCode: 1, Output: "can't find session: main"}, nil)
		if err := New(d).Kill(context.Background(), "c1", "main"); err != nil {
			t.Fatalf("kill gone session: %v", err)
		}
	})
	t.Run("real failure surfaces", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, false).
			Return(docker.ExecResult{ExitCode: 70, Output: "server exploded"}, nil)
		if err := New(d).Kill(context.Background(), "c1", "main"); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("invalid name never reaches docker", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		if err := New(d).Kill(context.Background(), "c1", "-x"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("err = %v, want ErrInvalidName", err)
		}
	})
}

// TestLaunchNamedPinsRestartArgv proves restart relaunches under the SAME
// session name (no suffix drift) through the full pipeline.
func TestLaunchNamedPinsRestartArgv(t *testing.T) {
	d := dockermocks.NewMockClient(t)
	ctx := context.Background()

	d.EXPECT().Exec(mock.Anything, "c1", []string{"test", "-d", "/workspace/repo/.git"}, false).
		Return(docker.ExecResult{ExitCode: 1}, nil)
	d.EXPECT().Exec(mock.Anything, "c1", []string{"bash", "-lc", "command -v fakecli"}, false).
		Return(docker.ExecResult{ExitCode: 0, Output: "found\n"}, nil)
	d.EXPECT().Exec(mock.Anything, "c1", mock.Anything, true).
		Return(docker.ExecResult{ExitCode: 0, Output: "1.0\n"}, nil)
	d.EXPECT().Exec(mock.Anything, "c1",
		append([]string{"tmux", "new-session", "-d", "-s", "fake-1", "-c", "/workspace",
			"bash", "-lc", `fakecli || echo "[fakecli exited: $?]"`,
			";", "set-option", "remain-on-exit", "on",
			";", "set-option", "-s", "escape-time", "0"}, ThemeArgs()...), false).
		Return(docker.ExecResult{ExitCode: 0}, nil)

	name, err := New(d).LaunchNamed(ctx, "c1", "fake-1", newHarness())
	if err != nil || name != "fake-1" {
		t.Fatalf("got (%q, %v), want fake-1", name, err)
	}
}

func TestInjectValidation(t *testing.T) {
	ctx := context.Background()
	t.Run("invalid name", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		if err := New(d).Inject(ctx, "c1", "-bad", "echo hi"); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("err = %v, want ErrInvalidName", err)
		}
	})
	t.Run("blank command is a sentinel and never touches docker", func(t *testing.T) {
		d := dockermocks.NewMockClient(t)
		for _, cmd := range []string{"", "   "} {
			if err := New(d).Inject(ctx, "c1", "main", cmd); !errors.Is(err, ErrEmptyCommand) {
				t.Fatalf("err = %v, want ErrEmptyCommand", err)
			}
		}
	})
}
