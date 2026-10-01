package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"pcoder/internal/auth"
	"pcoder/internal/config"
)

// smtpTestMain is the `pcoder smtp-test` entrypoint: load config from
// the environment (the compose project dir's .env in production) and
// probe delivery.
func smtpTestMain() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	if err := runSMTPTest(cfg); err != nil {
		slog.Error("smtp-test failed", "err", err)
		os.Exit(1)
	}
}

// runSMTPTest sends one delivery-probe email through auth.SmtpMailer to
// the login address and returns non-nil on failure. The setup script runs
// it after building the images and before booting: nothing goes live on
// bad credentials. SMTP_HOST/SMTP_PORT fall through to the config
// defaults, so tests point it at a fake server with plain env vars.
func runSMTPTest(cfg *config.Config) error {
	if cfg.SMTPUser == "" || cfg.SMTPPass == "" {
		return errors.New("SMTP_USER and SMTP_PASSWORD must be set (see README for the Gmail app-password steps)")
	}
	from := cfg.SMTPFrom
	if from == "" {
		from = cfg.SMTPUser
	}
	mailer := auth.SmtpMailer{
		Host: cfg.SMTPHost, Port: cfg.SMTPPort,
		User: cfg.SMTPUser, Password: cfg.SMTPPass, From: from,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	msg := fmt.Sprintf("Pocket Coder setup test — if you are reading this, PIN delivery to %s works.", cfg.LoginEmail)
	if err := mailer.SendTest(ctx, cfg.LoginEmail, msg); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "smtp-test: delivery to %s via %s:%d OK\n", cfg.LoginEmail, cfg.SMTPHost, cfg.SMTPPort)
	return nil
}
