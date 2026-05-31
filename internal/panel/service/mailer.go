package service

import (
	"fmt"
	"net/smtp"
	"strings"
)

// SMTPSettings is the SMTP configuration used to send mail.
type SMTPSettings struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
}

// SendTestEmail sends a short test message using the given SMTP settings. It uses
// PlainAuth (when a username is set) and lets net/smtp upgrade to TLS via
// STARTTLS when the server offers it — which covers the common submission port
// (587). Returns a descriptive error so the UI can show why a test failed.
func SendTestEmail(cfg SMTPSettings, to string) error {
	if cfg.Host == "" || cfg.Port == 0 {
		return fmt.Errorf("SMTP host and port are required")
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("a test recipient is required")
	}
	from := cfg.From
	if from == "" {
		from = cfg.Username
	}
	if from == "" {
		return fmt.Errorf("a From address (or username) is required")
	}

	fromHeader := from
	if cfg.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", cfg.FromName, from)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	b.WriteString("Subject: MaburVM SMTP test\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	b.WriteString("This is a test email from MaburVM. If you received it, your SMTP settings are working.\r\n")

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}
	if err := smtp.SendMail(addr, auth, from, []string{to}, []byte(b.String())); err != nil {
		return fmt.Errorf("SMTP send failed: %w", err)
	}
	return nil
}
