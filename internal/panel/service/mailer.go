package service

import (
	"encoding/json"
	"fmt"
	"net/smtp"
	"strings"

	"gorm.io/gorm"
)

// LoadSMTPSettings reads the admin-configured SMTP settings from the
// system_settings 'email' section (the same row the Settings > Email page saves).
// ok is false when no email settings have been configured yet.
func LoadSMTPSettings(db *gorm.DB) (cfg SMTPSettings, ok bool, err error) {
	var raw string
	q := db.Raw("SELECT data::text FROM system_settings WHERE section = 'email'").Scan(&raw)
	if q.Error != nil {
		return SMTPSettings{}, false, q.Error
	}
	if strings.TrimSpace(raw) == "" {
		return SMTPSettings{}, false, nil
	}
	var d struct {
		SMTPHost     string `json:"smtpHost"`
		SMTPPort     int    `json:"smtpPort"`
		SMTPUser     string `json:"smtpUser"`
		SMTPPassword string `json:"smtpPassword"`
		SMTPFrom     string `json:"smtpFrom"`
		SMTPFromName string `json:"smtpFromName"`
	}
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		return SMTPSettings{}, false, err
	}
	if d.SMTPHost == "" || d.SMTPPort == 0 {
		return SMTPSettings{}, false, nil
	}
	return SMTPSettings{
		Host: d.SMTPHost, Port: d.SMTPPort, Username: d.SMTPUser,
		Password: d.SMTPPassword, From: d.SMTPFrom, FromName: d.SMTPFromName,
	}, true, nil
}

// SendWelcomeEmail sends a new-account welcome message with the sign-in URL. It
// mirrors SendTestEmail's transport (PlainAuth + opportunistic STARTTLS).
func SendWelcomeEmail(cfg SMTPSettings, to, name, panelURL string) error {
	if cfg.Host == "" || cfg.Port == 0 {
		return fmt.Errorf("SMTP host and port are required")
	}
	if strings.TrimSpace(to) == "" {
		return fmt.Errorf("a recipient is required")
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
	greeting := "Hello"
	if strings.TrimSpace(name) != "" {
		greeting = "Hello " + name
	}
	if panelURL == "" {
		panelURL = "the MaburVM panel"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	b.WriteString("Subject: Welcome to MaburVM\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n\r\n")
	fmt.Fprintf(&b, "%s,\r\n\r\n", greeting)
	b.WriteString("Your MaburVM account has been created.\r\n\r\n")
	fmt.Fprintf(&b, "Sign in: %s\r\n", panelURL)
	fmt.Fprintf(&b, "Username: %s\r\n\r\n", to)
	b.WriteString("For security, the password was set by your administrator — please change it after your first sign-in.\r\n")

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
