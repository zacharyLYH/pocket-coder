package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"pcoder/internal/auth"
	"pcoder/internal/config"
	"pcoder/internal/state"
)

// setPasswordMain is the `pcoder set-password` entrypoint: read the
// password from stdin (piped by setup.sh — never an argv/env var), hash
// it with Argon2id, and persist only the hash in state.json, bumping the
// session generation so every issued cookie dies. Exit 0/1 without
// booting. Every setup run applies its password; there is no "keep old"
// path.
func setPasswordMain() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("invalid configuration", "err", err)
		os.Exit(1)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil {
		slog.Error("read password from stdin", "err", err)
		os.Exit(1)
	}
	password := strings.TrimSuffix(string(raw), "\n")
	password = strings.TrimSuffix(password, "\r")
	if password == "" {
		slog.Error("set-password: empty password on stdin")
		os.Exit(1)
	}
	if len(password) < 8 {
		slog.Error("set-password: password must be at least 8 characters")
		os.Exit(1)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		slog.Error("hash password", "err", err)
		os.Exit(1)
	}
	st, err := state.Open(cfg.DataDir, state.Bootstrap{AuthEmail: cfg.AuthEmail})
	if err != nil {
		slog.Error("open state", "err", err)
		os.Exit(1)
	}
	if err := st.SetPassword(cfg.AuthEmail, hash); err != nil {
		slog.Error("store password hash", "err", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "set-password: password updated; existing sessions invalidated")
}
