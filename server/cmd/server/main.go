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
	"pcoder/internal/config"
	"pcoder/internal/docker"
	"pcoder/internal/events"
	"pcoder/internal/harness"
	"pcoder/internal/httpapi"
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

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	// state.Open creates the data dir if missing. state.json is the single
	// source of truth for desired app state; env config seeds a fresh
	// document only — existing values win.
	st, err := state.Open(cfg.DataDir, state.Bootstrap{
		LoginEmail: cfg.LoginEmail,
		SMTP:       smtpFromConfig(cfg),
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
	svc := project.NewService(project.Open(st), dkr, ev)
	svc.SetSSHKeys(sshKeyStore)

	sessions := session.New(dkr)
	svc.SetInstaller(&harnessInstaller{harnesses: harnesses, sessions: sessions})
	previewManager := preview.NewManager(&preview.DockerFactory{Docker: dkr})

	// Sync running containers to match state.json (harness installs,
	// etc.). state.json is the source of truth; this one-pass reconcile
	// at boot replaces per-handler workarounds for container/state drift.
	if err := dkr.Ping(context.Background()); err == nil {
		svc.ReconcileState(context.Background())
	}

	ev.Append("boot", map[string]any{"version": version})
	if len(seeded) > 0 {
		ev.Append("harness.seed", map[string]any{"written": seeded})
	}
	logger.Info("data dir ready", "data_dir", cfg.DataDir, "seeded_harnesses", seeded)

	srv := &http.Server{Addr: cfg.Bind, Handler: httpapi.New(httpapi.Deps{
		Events: ev, Version: version, Auth: authSvc, Projects: svc,
		Sessions: sessions, Harnesses: harnesses,
		SSHKeys: sshKeyStore, State: st, Preview: previewManager,
	})}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening", "addr", cfg.Bind, "data_dir", cfg.DataDir)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server failed", "err", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		logger.Info("signal received, shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := previewManager.Close(shutdownCtx); err != nil {
			logger.Error("preview shutdown failed", "err", err)
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "err", err)
			os.Exit(1)
		}
		logger.Info("shutdown complete")
	}
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
