package auth

import (
	"context"
	"fmt"
	"io"
	"net/smtp"
)

// ConsoleMailer prints the PIN to a writer (dev: the server log). The log IS
// the delivery channel — no SMTP server involved.
type ConsoleMailer struct {
	Out io.Writer
}

func (m ConsoleMailer) SendPIN(_ context.Context, email, pin string) error {
	_, err := fmt.Fprintf(m.Out, "login PIN for %s: %s\n", email, pin)
	return err
}

// SmtpMailer sends the PIN via Google SMTP using an app password
// (net/smtp — no SMTP server to run, credentials come from SMTP_* env vars).
type SmtpMailer struct {
	Host     string
	Port     int
	User     string // gmail address
	Password string // app password
	From     string // sender address
}

func (m SmtpMailer) SendPIN(_ context.Context, email, pin string) error {
	auth := smtp.PlainAuth("", m.User, m.Password, m.Host)
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: Pocket Coder login PIN\r\n\r\nYour login PIN is %s (valid for 10 minutes).\r\n",
		m.From, email, pin)
	if err := smtp.SendMail(fmt.Sprintf("%s:%d", m.Host, m.Port), auth, m.From, []string{email}, []byte(msg)); err != nil {
		return fmt.Errorf("send pin email: %w", err)
	}
	return nil
}
