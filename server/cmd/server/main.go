// Command server is the Pocket Coder control plane: it serves the
// HTTP API and talks to Docker on the host. This file only boots: config,
// data dir, state file, event log, harness seeding. The HTTP surface lives
// in internal/httpapi.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"pcoder/internal/auth"
	"pcoder/internal/butlerthreads"
	"pcoder/internal/codemapthreads"
	"pcoder/internal/config"
	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/httpapi"
	"pcoder/internal/obs"
	"pcoder/internal/preview"
	"pcoder/internal/project"
	"pcoder/internal/session"
	"pcoder/internal/sshkeys"
	"pcoder/internal/state"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	// state.Open creates the data dir if missing. state.json is the single
	// source of truth for desired app state; env config seeds a fresh
	// document only — existing values win.
	st, err := state.Open(cfg.DataDir, state.Bootstrap{
		LoginEmail: cfg.LoginEmail,
		SMTP:       smtpFromConfig(cfg),
		AI:         aiFromConfig(cfg),
	})
	if err != nil {
		slog.Error("open state", "err", err)
		os.Exit(1)
	}

	ev, err := events.Open(filepath.Join(cfg.DataDir, "events.log"))
	if err != nil {
		slog.Error("open event log", "err", err)
		os.Exit(1)
	}
	defer ev.Close()

	// Staged logging writes project lines to the observe file + stderr.
	// events.log is NOT a destination: it holds only the global audit
	// (boot, login, ssh keys — actions with no project), written by
	// explicit Events.Append calls. Project detail lives in observe logs.
	observe := obs.NewStore(cfg.DataDir)
	defer observe.Close()
	obs.Configure(observe, nil)

	harnesses := harness.New(st)
	seeded, err := harnesses.EnsureBuiltins()
	if err != nil {
		slog.Error("seed harnesses", "err", err)
		os.Exit(1)
	}

	authSvc, err := newAuthService(cfg, st)
	if err != nil {
		slog.Error("init auth", "err", err)
		os.Exit(1)
	}

	dkr, err := docker.New(cfg.DockerSock)
	if err != nil {
		slog.Error("init docker client", "err", err)
		os.Exit(1)
	}
	// Docker being down must not take auth or the event log with it: warn
	// now, fail per-operation when a project pipeline actually needs it.
	if err := dkr.Ping(context.Background()); err != nil {
		slog.Warn("docker engine unreachable", "err", err)
	}

	sshKeyStore := sshkeys.New(st)
	svc := project.NewService(project.Open(st), dkr)
	svc.SetSSHKeys(sshKeyStore)
	svc.SetGit(func() (string, string, string) {
		var name, email, token string
		st.View(func(doc *state.Document) {
			if g := state.FirstGit(doc); g != nil {
				name, email, token = g.Name, g.Email, g.Token
			}
		})
		return name, email, token
	})
	svc.SetAllowAnyRepo(cfg.AllowAnyRepo)

	// Codemap chats are project-scoped artifacts: deleting a project
	// deletes its chats (threads + lineage) from the data dir.
	codemapStore := codemapthreads.New(filepath.Join(cfg.DataDir, "codemaps"))
	svc.SetCodemaps(codemapStore)

	sessions := session.New(dkr)
	svc.SetInstaller(&harnessInstaller{harnesses: harnesses, sessions: sessions})
	previewManager := preview.NewManager(&preview.DockerFactory{Docker: dkr})
	previewManager.StartSweeper(previewSweepInterval(), previewTokenSilence())

	// Bootstrap before serving: make live Docker match state.json for every
	// project — containers running, recorded harnesses installed. Blocking on
	// purpose: slow installs (npm downloads) happen here, on the boot log,
	// so no request can ever observe a missing container or binary.
	if err := dkr.Ping(context.Background()); err == nil {
		if err := svc.BringAllUp(context.Background()); err != nil {
			slog.Warn("bootstrap finished with errors — serving anyway; the per-request safety net (EnsureContainer) will retry", "err", err)
		}
	} else {
		slog.Warn("docker engine unreachable at boot — skipping bootstrap; EnsureContainer will reconcile per request", "err", err)
	}

	ev.Append("boot", map[string]any{"version": version})
	if len(seeded) > 0 {
		ev.Append("harness.seed", map[string]any{"written": seeded})
	}
	slog.Info("data dir ready", "data_dir", cfg.DataDir, "seeded_harnesses", seeded)

	srv := &http.Server{Addr: cfg.Bind, Handler: httpapi.New(httpapi.Deps{
		Events: ev, Version: version, Auth: authSvc, Projects: svc,
		Sessions: sessions, Harnesses: harnesses,
		SSHKeys: sshKeyStore, State: st, Preview: previewManager,
		Obs: observe, Docker: dkr, Codemaps: codemapStore,
		Butler: butlerthreads.New(filepath.Join(cfg.DataDir, "butler")),
	})}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", cfg.Bind, "data_dir", cfg.DataDir)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server failed", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		slog.Info("signal received, shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := previewManager.Close(shutdownCtx); err != nil {
			slog.Error("preview shutdown failed", "err", err)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
		slog.Info("shutdown complete")
	}
}

// previewSweepInterval bounds how stale a token's presence can look;
// previewTokenSilence bounds how long a holder may go quiet before rotation.
func previewSweepInterval() time.Duration {
	return durationEnv("PCODER_PREVIEW_SWEEP_INTERVAL", 30*time.Second)
}

func previewTokenSilence() time.Duration {
	return durationEnv("PCODER_PREVIEW_TOKEN_SILENCE", 3*time.Minute)
}

func durationEnv(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return def
}

// harnessInstaller adapts session installation for project recovery without
// letting the project package import session (the SetSSHKeys pattern).
type harnessInstaller struct {
	harnesses *harness.Store
	sessions  *session.Service
}

func (h *harnessInstaller) InstallHarness(ctx context.Context, container string, harnessID string) error {
	har, err := h.harnesses.Get(harnessID)
	if err != nil {
		// unknown harness — record names an id that no longer exists, skip it
		return nil
	}
	return h.sessions.InstallHarness(ctx, container, har)
}

func smtpFromConfig(cfg *config.Config) *state.SMTP {
	if cfg.SMTPUser == "" || cfg.SMTPPass == "" {
		return nil
	}
	return &state.SMTP{Host: cfg.SMTPHost, Port: cfg.SMTPPort, User: cfg.SMTPUser, Password: cfg.SMTPPass, From: cfg.SMTPFrom}
}

// aiFromConfig seeds the shared model list from env. Any part
// missing means no seed: the settings form fills it later.
func aiFromConfig(cfg *config.Config) *state.AIModel {
	if cfg.AIBaseURL == "" || cfg.AIAPIKey == "" || cfg.AIModel == "" {
		return nil
	}
	return &state.AIModel{ID: "default", Label: "Default", BaseURL: cfg.AIBaseURL, APIKey: cfg.AIAPIKey, Model: cfg.AIModel}
}

// newAuthService builds the auth service from state (email + SMTP creds,
// seeded from env on first boot): PIN delivery by SMTP when configured,
// console (server log) otherwise.
func newAuthService(cfg *config.Config, st *state.Store) (*auth.Service, error) {
	secret := []byte(cfg.JWTSecret)
	if len(secret) == 0 {
		var err error
		secret, err = auth.LoadOrCreateSecret(filepath.Join(cfg.DataDir, "jwt-secret"))
		if err != nil {
			return nil, err
		}
	}
	var (
		email    string
		smtpCfg  *state.SMTP
		mailer   auth.Mailer = auth.ConsoleMailer{Out: os.Stderr}
		mailerNm             = "console"
	)
	st.View(func(doc *state.Document) {
		email = doc.User.Email
		smtpCfg = doc.SMTP
	})
	if smtpCfg != nil && smtpCfg.User != "" && smtpCfg.Password != "" {
		mailer = auth.SmtpMailer{Host: smtpCfg.Host, Port: smtpCfg.Port, User: smtpCfg.User, Password: smtpCfg.Password, From: smtpCfg.From}
		mailerNm = "smtp"
	}
	svc := auth.New(email, secret, mailer)
	svc.MailerName = mailerNm
	slog.Info("auth ready", "login_email", email, "mailer", mailerNm)
	return svc, nil
}
