package main

import (
	"fmt"
	"io"
	"log/slog"
	"net/mail"
	"os"
	"strings"

	"pcoder/internal/auth"
	"pcoder/internal/config"
	"pcoder/internal/state"
)

// setPasswordMain is the `pcoder set-password [--email addr]` entrypoint:
// read the password from stdin (piped by setup.sh — never an argv/env var),
// hash it with Argon2id, and persist the email + only the hash in
// state.json, bumping the session generation so every issued cookie dies.
// Exit 0/1 without booting. Every setup run applies its password; there is
// no "keep old" path.
//
// state.json is the single identity source, so --email is only needed on a
// fresh install (no state yet). On a rerun the stored email is kept unless
// the flag overrides it.
func setPasswordMain() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	email, err := emailFlag(os.Args[2:])
	if err != nil {
		slog.Error("set-password", "err", err)
		os.Exit(2)
	}
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
	st, err := state.Open(cfg.DataDir, state.Bootstrap{AuthEmail: email})
	if err != nil {
		slog.Error("open state", "err", err)
		os.Exit(1)
	}
	if email == "" {
		st.View(func(doc *state.Document) { email = doc.User.Email })
	}
	if email == "" {
		slog.Error("set-password: --email is required on a fresh install (state.json has no account)")
		os.Exit(1)
	}
	if err := st.SetPassword(email, hash); err != nil {
		slog.Error("store password hash", "err", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "set-password: password updated; existing sessions invalidated")
}

// emailFlag parses `--email addr` / `--email=addr`; empty means "keep the
// stored email". The address is normalized once here, the same way login
// normalizes it, so the two can never drift.
func emailFlag(args []string) (string, error) {
	var email string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--email":
			if i+1 >= len(args) {
				return "", fmt.Errorf("--email needs a value")
			}
			email = args[i+1]
			i++
		case strings.HasPrefix(arg, "--email="):
			email = strings.TrimPrefix(arg, "--email=")
		default:
			return "", fmt.Errorf("unknown argument %q (usage: set-password [--email addr])", arg)
		}
	}
	email = auth.NormalizeEmail(email)
	if email == "" {
		return "", nil
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", fmt.Errorf("--email %q is not a valid email address", email)
	}
	return email, nil
}
