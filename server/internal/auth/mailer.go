package auth

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"time"
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

func (m SmtpMailer) SendPIN(ctx context.Context, email, pin string) error {
	return m.send(ctx, email, "Pocket Coder login PIN",
		fmt.Sprintf("Your login PIN is %s (valid for 10 minutes).\r\n", pin))
}

// SendTest delivers a plain body with a setup-test subject: the setup
// script's delivery probe (a PIN send would read like a real login).
func (m SmtpMailer) SendTest(ctx context.Context, email, body string) error {
	return m.send(ctx, email, "Pocket Coder setup test", body+"\r\n")
}

func (m SmtpMailer) send(ctx context.Context, email, subject, body string) error {
	// One deadline for the whole send: the caller's when present (the
	// setup probe passes 30s), otherwise a sane default. The dial and
	// every subsequent read/write share it, so a black-holed endpoint
	// can hang neither the probe nor a login request. This mirrors
	// smtp.SendMail (EHLO, opportunistic STARTTLS, auth, send, quit).
	deadline := time.Now().Add(30 * time.Second)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl
	}
	dialFor := time.Until(deadline)
	if dialFor > 10*time.Second {
		dialFor = 10 * time.Second
	}
	if dialFor <= 0 {
		return fmt.Errorf("send email: %w", ctx.Err())
	}
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", m.Host, m.Port), dialFor)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: m.Host}); err != nil {
			return fmt.Errorf("send email: %w", err)
		}
	}
	auth := smtp.PlainAuth("", m.User, m.Password, m.Host)
	if err := c.Auth(auth); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s",
		m.From, email, subject, body)
	if err := c.Mail(m.From); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if err := c.Rcpt(email); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("send email: %w", err)
	}
	return nil
}
