// Package email — minimal SMTP sender.
//
// Talks to MailHog (no auth) in dev, real SMTP in prod. Builds multipart
// messages with text + HTML alternative parts.
//
// Intentionally tiny: no template engine, no retry queue, no MIME complexity
// beyond what's needed for transactional OTP messages. When we add the
// Notification service, that becomes the system-wide email path and this
// package stays as an in-process fallback.

package email

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Host     string // empty → email is disabled
	Port     int
	Username string
	Password string
	From     string // "no-reply@nexussacco.local"
	FromName string // "nexusSacco"
	UseTLS   bool   // STARTTLS upgrade after EHLO
}

type Sender interface {
	Send(msg Message) error
	Enabled() bool
}

type Message struct {
	To       []string
	Subject  string
	Text     string
	HTML     string
	ReplyTo  string
}

type smtpSender struct {
	cfg Config
}

func New(cfg Config) Sender {
	return &smtpSender{cfg: cfg}
}

func (s *smtpSender) Enabled() bool { return s.cfg.Host != "" }

func (s *smtpSender) Send(msg Message) error {
	if !s.Enabled() {
		return errors.New("email: SMTP not configured")
	}
	if len(msg.To) == 0 {
		return errors.New("email: at least one recipient required")
	}

	from := s.cfg.From
	if from == "" {
		return errors.New("email: SMTP_FROM not set")
	}

	body := buildMIME(s.cfg, msg)
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))

	var auth smtp.Auth
	if s.cfg.Username != "" {
		auth = smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)
	}

	// MailHog accepts plain SMTP without STARTTLS — use simple SendMail.
	if !s.cfg.UseTLS {
		return smtp.SendMail(addr, auth, from, msg.To, body)
	}

	// STARTTLS path for real SMTP servers.
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("dial smtp: %w", err)
	}
	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		return fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = c.Quit() }()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host}); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if auth != nil {
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	for _, to := range msg.To {
		if err := c.Rcpt(to); err != nil {
			return fmt.Errorf("rcpt %s: %w", to, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	return w.Close()
}

// buildMIME constructs a multipart/alternative message.
func buildMIME(cfg Config, m Message) []byte {
	var b strings.Builder

	fromHeader := cfg.From
	if cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", mime.QEncoding.Encode("utf-8", cfg.FromName), cfg.From)
	}
	b.WriteString("From: " + fromHeader + "\r\n")
	b.WriteString("To: " + strings.Join(m.To, ", ") + "\r\n")
	if m.ReplyTo != "" {
		b.WriteString("Reply-To: " + m.ReplyTo + "\r\n")
	}
	b.WriteString("Subject: " + mime.QEncoding.Encode("utf-8", m.Subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")

	if m.HTML == "" {
		b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n\r\n")
		b.WriteString(m.Text)
		return []byte(b.String())
	}

	const boundary = "----=_NextPart_nexusSacco_alt"
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(m.Text + "\r\n\r\n")

	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=\"utf-8\"\r\nContent-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(m.HTML + "\r\n\r\n")

	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String())
}
