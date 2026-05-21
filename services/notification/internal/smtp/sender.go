// Minimal SMTP sender. Supports plain text (with a small HTML wrap),
// PLAIN auth, and three encryption modes:
//   • none      — plain TCP (Mailpit dev catcher on :1025)
//   • starttls  — connect plain, upgrade via STARTTLS
//   • tls       — implicit TLS (SMTPS, typically port 465)
//
// Attachments + multi-recipient are deferred to Stage 5 (PDFs) and
// Stage 7 (campaigns). For now: one address, one message.

package smtp

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"html"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/nexussacco/notification/internal/domain"
)

type Attachment struct {
	Filename string
	Data     []byte
	MimeType string // defaults to application/pdf
}

type Message struct {
	From        string // "Sender Name <user@example.com>"
	ReplyTo     string
	To          string // "Member Name <member@example.com>"
	Subject     string
	PlainBody   string
	Attachments []Attachment
}

// Send delivers one message via the supplied SMTP config. Returns
// (providerMessageID, error). Most servers (Mailpit included) don't
// return a message id; in that case we return our own RFC822-style
// Message-ID header so the audit log has something to display.
func Send(cfg *domain.SMTPConfig, msg Message) (string, error) {
	if cfg == nil {
		return "", errors.New("smtp: nil config")
	}
	if cfg.Host == "" || cfg.Port == 0 {
		return "", errors.New("smtp: host and port are required")
	}
	if msg.To == "" || msg.From == "" {
		return "", errors.New("smtp: from and to are required")
	}
	if msg.Subject == "" {
		msg.Subject = "(no subject)"
	}
	if msg.From == "" && cfg.FromAddress != "" {
		msg.From = fromHeader(cfg.FromName, cfg.FromAddress)
	}
	if msg.ReplyTo == "" && cfg.ReplyTo != nil && *cfg.ReplyTo != "" {
		msg.ReplyTo = *cfg.ReplyTo
	}

	messageID := fmt.Sprintf("<%d.%d@%s>", time.Now().UnixNano(), len(msg.PlainBody), cfg.Host)
	mime := buildMIME(msg, messageID)
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	switch cfg.Encryption {
	case domain.SMTPTLS:
		return messageID, sendImplicitTLS(addr, cfg, msg, mime)
	case domain.SMTPStartTLS:
		return messageID, sendSTARTTLS(addr, cfg, msg, mime)
	default:
		return messageID, sendPlain(addr, cfg, msg, mime)
	}
}

// ─────────── Wire format ───────────

func buildMIME(msg Message, messageID string) []byte {
	// With attachments: multipart/mixed { multipart/alternative; attachments... }
	// Without:          multipart/alternative
	altBoundary := "==NXALT=="
	mixedBoundary := "==NXMIX=="
	hasAttach := len(msg.Attachments) > 0

	var b strings.Builder
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("From: " + msg.From + "\r\n")
	b.WriteString("To: " + msg.To + "\r\n")
	if msg.ReplyTo != "" {
		b.WriteString("Reply-To: " + msg.ReplyTo + "\r\n")
	}
	b.WriteString("Subject: " + msg.Subject + "\r\n")
	b.WriteString("Message-ID: " + messageID + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	if hasAttach {
		b.WriteString(fmt.Sprintf("Content-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n", mixedBoundary))
		b.WriteString("--" + mixedBoundary + "\r\n")
	}
	b.WriteString(fmt.Sprintf("Content-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n", altBoundary))

	// Plain part.
	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(msg.PlainBody)
	b.WriteString("\r\n\r\n")

	// HTML part.
	b.WriteString("--" + altBoundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
	b.WriteString(htmlWrap(msg.PlainBody))
	b.WriteString("\r\n\r\n")
	b.WriteString("--" + altBoundary + "--\r\n")

	if hasAttach {
		for _, a := range msg.Attachments {
			mt := a.MimeType
			if mt == "" {
				mt = "application/pdf"
			}
			b.WriteString("--" + mixedBoundary + "\r\n")
			b.WriteString("Content-Type: " + mt + "; name=\"" + a.Filename + "\"\r\n")
			b.WriteString("Content-Disposition: attachment; filename=\"" + a.Filename + "\"\r\n")
			b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
			b.WriteString(base64Wrap(a.Data, 76))
			b.WriteString("\r\n")
		}
		b.WriteString("--" + mixedBoundary + "--\r\n")
	}
	return []byte(b.String())
}

func base64Wrap(data []byte, lineLen int) string {
	enc := base64.StdEncoding.EncodeToString(data)
	var out strings.Builder
	for i := 0; i < len(enc); i += lineLen {
		end := i + lineLen
		if end > len(enc) {
			end = len(enc)
		}
		out.WriteString(enc[i:end])
		out.WriteString("\r\n")
	}
	return out.String()
}

func htmlWrap(plain string) string {
	body := html.EscapeString(plain)
	body = strings.ReplaceAll(body, "\n", "<br>\n")
	return `<!DOCTYPE html>
<html><body style="font-family: -apple-system, Segoe UI, Helvetica, Arial, sans-serif; font-size: 14px; line-height: 1.5; color: #222; padding: 20px; max-width: 600px;">
<div style="border-bottom: 2px solid #2c5282; padding-bottom: 10px; margin-bottom: 16px; font-weight: 600; font-size: 16px; color: #2c5282;">Notification</div>
<div>` + body + `</div>
<div style="margin-top: 24px; padding-top: 12px; border-top: 1px solid #eee; font-size: 12px; color: #888;">This is an automated message from your SACCO management platform.</div>
</body></html>`
}

func fromHeader(name, addr string) string {
	if name == "" {
		return addr
	}
	return name + " <" + addr + ">"
}

// ─────────── Transports ───────────

func sendPlain(addr string, cfg *domain.SMTPConfig, msg Message, mime []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	return finishSend(c, cfg, msg, mime, nil)
}

func sendSTARTTLS(addr string, cfg *domain.SMTPConfig, msg Message, mime []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return errors.New("smtp: STARTTLS not advertised by server")
	}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	if err := c.StartTLS(tlsCfg); err != nil {
		return fmt.Errorf("starttls: %w", err)
	}
	return finishSend(c, cfg, msg, mime, tlsCfg)
}

func sendImplicitTLS(addr string, cfg *domain.SMTPConfig, msg Message, mime []byte) error {
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}
	conn, err := tls.Dial("tcp", addr, tlsCfg)
	if err != nil {
		return fmt.Errorf("tls dial: %w", err)
	}
	c, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp client: %w", err)
	}
	defer c.Close()
	return finishSend(c, cfg, msg, mime, tlsCfg)
}

func finishSend(c *smtp.Client, cfg *domain.SMTPConfig, msg Message, mime []byte, tlsCfg *tls.Config) error {
	_ = tlsCfg // kept for the StartTLS/TLS branches that need it earlier
	if cfg.Username != "" {
		auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
		if err := c.Auth(auth); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	fromAddr := stripAngleAddr(msg.From)
	toAddr := stripAngleAddr(msg.To)
	if err := c.Mail(fromAddr); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := c.Rcpt(toAddr); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write(mime); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close data: %w", err)
	}
	return c.Quit()
}

// stripAngleAddr returns just "user@host" from "Name <user@host>".
func stripAngleAddr(addr string) string {
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.LastIndex(addr, ">"); j > i {
			return addr[i+1 : j]
		}
	}
	return strings.TrimSpace(addr)
}
