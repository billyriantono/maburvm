package service

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/mail"
	"net/smtp"
	"os"
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
		Brand: LoadBranding(db), Templates: LoadEmailTemplates(db),
	}, true, nil
}

// SMTPSettings is the SMTP configuration used to send mail, with the branding
// and template edits a message is rendered from.
type SMTPSettings struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
	FromName string
	// Brand and Templates come from the same database read as the SMTP
	// settings, so one load is enough to send a finished message.
	Brand     Branding
	Templates map[string]EmailTemplate
}

// Template returns the operator's version of a template, or the built-in one.
func (c SMTPSettings) Template(key string) EmailTemplate {
	if t, ok := c.Templates[key]; ok && t.Subject != "" && t.Body != "" {
		return t
	}
	def, _ := EmailTemplateDefFor(key)
	return def.Default
}

// EmailLogoURL is the address a mail client fetches the logo from. Mail
// clients do not show images embedded in the message, so the logo is only used
// when the panel knows its public address; SVG is skipped because most mail
// clients will not draw it.
func EmailLogoURL(b Branding, publicURL string) string {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if b.Logo == "" || publicURL == "" || strings.HasPrefix(b.Logo, "data:image/svg") {
		return ""
	}
	return publicURL + "/api/v1/branding/logo"
}

// SendWelcomeEmail sends the new-account message with a link to sign in.
func SendWelcomeEmail(cfg SMTPSettings, to, name, panelURL string) error {
	link := ""
	if panelURL != "" {
		link = strings.TrimRight(panelURL, "/") + "/login"
	}
	return SendMail(cfg, to, RenderEmail(cfg.Template(EmailWelcome), cfg.Brand,
		EmailData{Name: name, Email: to, Link: link, LogoURL: EmailLogoURL(cfg.Brand, panelURL)}))
}

// SendPasswordResetEmail sends a password-reset link.
func SendPasswordResetEmail(cfg SMTPSettings, to, name, resetURL string) error {
	return SendMail(cfg, to, RenderEmail(cfg.Template(EmailPasswordReset), cfg.Brand,
		EmailData{Name: name, Email: to, Link: resetURL, LogoURL: EmailLogoURL(cfg.Brand, os.Getenv("PANEL_PUBLIC_URL"))}))
}

// SendTestEmail sends a short message that proves the SMTP settings work.
func SendTestEmail(cfg SMTPSettings, to string) error {
	if cfg.Brand.Name == "" {
		cfg.Brand.Name = DefaultBrandName
	}
	return SendMail(cfg, to, RenderEmail(EmailTemplate{
		Subject: "{{brand}} SMTP test",
		Body:    "This is a test email from {{brand}}. If you received it, your SMTP settings are working.",
	}, cfg.Brand, EmailData{Email: to}))
}

// SendMail sends one rendered message as HTML with a plain-text alternative.
// It uses PlainAuth when a username is set and lets net/smtp upgrade to TLS
// through STARTTLS when the server offers it (the common submission port, 587).
// Errors are descriptive so the UI can say why a send failed.
func SendMail(cfg SMTPSettings, to string, msg RenderedEmail) error {
	if cfg.Host == "" || cfg.Port == 0 {
		return fmt.Errorf("SMTP host and port are required")
	}
	to = oneLine(to)
	if to == "" {
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
	if name := oneLine(cfg.FromName); name != "" {
		fromHeader = (&mail.Address{Name: name, Address: from}).String()
	}

	const boundary = "maburvm-alt-7f3c9a"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", fromHeader)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", oneLine(msg.Subject)))
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	for _, part := range []struct{ kind, body string }{{"text/plain", msg.Text}, {"text/html", msg.HTML}} {
		fmt.Fprintf(&b, "--%s\r\nContent-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", boundary, part.kind)
		enc := base64.StdEncoding.EncodeToString([]byte(part.body))
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)

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
