package service

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"strconv"
	"strings"

	"gorm.io/gorm"
)

// EmailTemplatesSection is the system_settings row holding the operator's
// edits to the emails the panel sends, keyed by template.
const EmailTemplatesSection = "email_templates"

// EmailTemplate is the editable part of one email. The body is plain text;
// the panel wraps it in the branded layout, so an operator cannot break the
// markup or inject any.
type EmailTemplate struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Button is the label of the one action link (sign in, reset password).
	// Where it points is fixed per template.
	Button string `json:"button"`
	// Off stops this email being sent. Only honoured for templates marked
	// Optional: turning off a password reset would lock people out.
	Off bool `json:"off,omitempty"`
}

// EmailTemplateDef describes one email the panel sends.
type EmailTemplateDef struct {
	Key         string            `json:"key"`
	Name        string            `json:"name"`
	SentWhen    string            `json:"sent_when"`
	Default     EmailTemplate     `json:"default"`
	Sample      map[string]string `json:"sample"`
	SampleLink  string            `json:"sample_link"`
	LinkMeaning string            `json:"link_meaning"`
	// Optional emails can be switched off by the operator, for example when
	// their billing system already sends its own.
	Optional bool `json:"optional"`
}

// EmailPlaceholders are the words an operator can drop into a subject or body.
var EmailPlaceholders = []struct {
	Token   string `json:"token"`
	Meaning string `json:"meaning"`
}{
	{"{{greeting}}", "“Hello” plus the person's name when the panel knows it"},
	{"{{name}}", "The person's name (may be empty)"},
	{"{{email}}", "The address the email is sent to"},
	{"{{brand}}", "Your panel name"},
	{"{{server}}", "The server's name (server emails only)"},
	{"{{address}}", "The server's IP address (server emails only)"},
	{"{{system}}", "The operating system installed (server emails only)"},
	{"{{access}}", "How to sign in to the server: where to read its password, or a reminder of what was chosen (server emails only)"},
}

const (
	EmailWelcome       = "welcome"
	EmailPasswordReset = "password_reset"
	EmailVMReady       = "vm_ready"
)

var emailTemplateDefs = []EmailTemplateDef{
	{
		Key:      EmailWelcome,
		Name:     "Welcome",
		SentWhen: "Sent when an administrator creates an account for someone.",
		Default: EmailTemplate{
			Subject: "Welcome to {{brand}}",
			Body: "{{greeting}},\n\nYour {{brand}} account has been created.\n\nUsername: {{email}}\n\n" +
				"For security, the password was set by your administrator. Please change it after your first sign-in.",
			Button: "Sign in",
		},
		Sample:      map[string]string{"name": "Dewi", "email": "dewi@example.com"},
		SampleLink:  "https://panel.example.com/login",
		LinkMeaning: "The button opens your panel's sign-in page.",
	},
	{
		Key:      EmailPasswordReset,
		Name:     "Password reset",
		SentWhen: "Sent when someone asks to reset their password.",
		Default: EmailTemplate{
			Subject: "Reset your {{brand}} password",
			Body: "{{greeting}},\n\nWe received a request to reset your {{brand}} password. The button below works for one hour.\n\n" +
				"If you didn't ask for this, ignore this email and your password stays the same.",
			Button: "Reset password",
		},
		Sample:      map[string]string{"name": "Dewi", "email": "dewi@example.com"},
		SampleLink:  "https://panel.example.com/reset-password?token=example",
		LinkMeaning: "The button opens the page where they choose a new password.",
	},
	{
		Key:      EmailVMReady,
		Name:     "Server ready",
		SentWhen: "Sent to a server's owner when it has been created and started. The root password is never in the email: it is read on the server's page after signing in.",
		Optional: true,
		Default: EmailTemplate{
			Subject: "Your server {{server}} is ready",
			Body: "{{greeting}},\n\nYour server {{server}} has been created and is running.\n\n" +
				"Address: {{address}}\nSystem: {{system}}\nUser: root\n\n{{access}}",
			Button: "Open your server",
		},
		Sample: map[string]string{
			"name": "Dewi", "email": "dewi@example.com", "server": "web-01", "address": "203.0.113.24", "system": "Ubuntu 24.04 LTS",
			"access": AccessPasswordOnPage,
		},
		SampleLink:  "https://panel.example.com/client/vms/example",
		LinkMeaning: "The button opens the server's page in your panel, where its root password can be read after signing in.",
	},
}

// What {{access}} becomes in a server email, by how the server can be reached.
const (
	// The panel generated the password and is keeping it for the owner.
	AccessPasswordOnPage = "For your safety the root password is not in this email. Open the server's page and sign in to read it. " +
		"It can be read there for 72 hours, and every time it is read is recorded. After that it is erased, and you can set a new one from the same page."
	// The owner chose the password themselves when ordering.
	AccessOwnPassword = "Sign in with the root password you chose when you ordered it. If you have lost it, you can set a new one from the server's page."
)

// EmailTemplateDefs lists every email the panel sends.
func EmailTemplateDefs() []EmailTemplateDef { return emailTemplateDefs }

// EmailTemplateDefFor finds a definition by key.
func EmailTemplateDefFor(key string) (EmailTemplateDef, bool) {
	for _, d := range emailTemplateDefs {
		if d.Key == key {
			return d, true
		}
	}
	return EmailTemplateDef{}, false
}

// Normalize trims the template and reports the first problem in plain words.
func (t *EmailTemplate) Normalize() error {
	t.Subject = strings.TrimSpace(t.Subject)
	t.Button = strings.TrimSpace(t.Button)
	t.Body = strings.TrimSpace(strings.ReplaceAll(t.Body, "\r\n", "\n"))
	switch {
	case t.Subject == "":
		return fmt.Errorf("The subject is empty")
	case strings.ContainsAny(t.Subject, "\r\n"):
		return fmt.Errorf("The subject must be a single line")
	case len([]rune(t.Subject)) > 150:
		return fmt.Errorf("The subject is longer than 150 characters")
	case t.Body == "":
		return fmt.Errorf("The message is empty")
	case len([]rune(t.Body)) > 4000:
		return fmt.Errorf("The message is longer than 4000 characters")
	case strings.ContainsAny(t.Button, "\r\n") || len([]rune(t.Button)) > 40:
		return fmt.Errorf("The button label must be one line of at most 40 characters")
	}
	return nil
}

// LoadEmailTemplates returns the operator's saved edits, keyed by template.
func LoadEmailTemplates(db *gorm.DB) map[string]EmailTemplate {
	out := map[string]EmailTemplate{}
	if db == nil {
		return out
	}
	var raw string
	if err := db.Raw("SELECT data::text FROM system_settings WHERE section = ?", EmailTemplatesSection).Scan(&raw).Error; err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &out)
	}
	return out
}

// SaveEmailTemplate stores an edit; a nil template removes it, which puts the
// built-in wording back.
func SaveEmailTemplate(db *gorm.DB, key string, t *EmailTemplate) error {
	if _, ok := EmailTemplateDefFor(key); !ok {
		return fmt.Errorf("unknown email template %q", key)
	}
	all := LoadEmailTemplates(db)
	if t == nil {
		delete(all, key)
	} else {
		if err := t.Normalize(); err != nil {
			return err
		}
		all[key] = *t
	}
	data, _ := json.Marshal(all)
	return db.Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		EmailTemplatesSection, string(data),
	).Error
}

// RenderedEmail is one message ready to send.
type RenderedEmail struct {
	Subject string `json:"subject"`
	Text    string `json:"text"`
	HTML    string `json:"html"`
}

// EmailData is what one send fills a template with.
type EmailData struct {
	Name  string
	Email string
	// Link is where the button goes; empty hides the button.
	Link string
	// LogoURL is an absolute address a mail client can fetch; empty shows the
	// brand name as text instead.
	LogoURL string
	// Extra fills the placeholders only some emails have, such as {{server}}.
	// A key without braces: "server" fills {{server}}.
	Extra map[string]string
}

func fillPlaceholders(s string, b Branding, d EmailData) string {
	greeting := "Hello"
	if n := strings.TrimSpace(d.Name); n != "" {
		greeting = "Hello " + n
	}
	pairs := []string{
		"{{greeting}}", greeting,
		"{{name}}", strings.TrimSpace(d.Name),
		"{{email}}", d.Email,
		"{{brand}}", b.Name,
	}
	for k, v := range d.Extra {
		pairs = append(pairs, "{{"+k+"}}", v)
	}
	return strings.NewReplacer(pairs...).Replace(s)
}

// oneLine keeps a value safe to put in a mail header: a name or a brand with
// a line break in it must not be able to start a new header.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(s)), " ")
}

// inkOn returns black or white, whichever reads better on the colour.
func inkOn(hex string) string {
	lin := func(i int) float64 {
		v, _ := strconv.ParseInt(hex[i:i+2], 16, 0)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	l := 0.2126*lin(1) + 0.7152*lin(3) + 0.0722*lin(5)
	if (1.05)/(l+0.05) >= (l+0.05)/0.05 {
		return "#ffffff"
	}
	return "#0a0a0a"
}

// RenderEmail fills a template and wraps it in the branded layout. Everything
// that came from a person is escaped; the markup is ours alone.
func RenderEmail(t EmailTemplate, b Branding, d EmailData) RenderedEmail {
	if b.Name == "" {
		b.Name = DefaultBrandName
	}
	accent := b.Accent
	if !hexColor.MatchString(accent) {
		accent = "#18181b"
	}
	subject := oneLine(fillPlaceholders(t.Subject, b, d))
	body := fillPlaceholders(t.Body, b, d)

	text := body
	if d.Link != "" {
		label := t.Button
		if label == "" {
			label = "Open"
		}
		text += "\n\n" + label + ": " + d.Link
	}
	footer := b.FooterText
	if footer == "" {
		footer = b.Name
	}
	text += "\n\n-- \n" + footer
	if b.SupportEmail != "" {
		text += "\n" + b.SupportEmail
	}

	e := html.EscapeString
	var h strings.Builder
	h.WriteString(`<!doctype html><html><body style="margin:0;padding:0;background:#f4f4f5;">`)
	h.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f4f4f5;padding:32px 12px;"><tr><td align="center">`)
	h.WriteString(`<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;width:100%;background:#ffffff;border:1px solid #e4e4e7;border-radius:10px;font-family:-apple-system,'Segoe UI',Roboto,Helvetica,Arial,sans-serif;color:#18181b;">`)
	h.WriteString(`<tr><td style="padding:28px 32px 0 32px;">`)
	if d.LogoURL != "" {
		fmt.Fprintf(&h, `<img src="%s" alt="%s" height="36" style="height:36px;max-width:220px;border:0;display:block;">`, e(d.LogoURL), e(b.Name))
	} else {
		fmt.Fprintf(&h, `<div style="font-size:20px;font-weight:700;letter-spacing:-0.01em;">%s</div>`, e(b.Name))
	}
	fmt.Fprintf(&h, `<div style="height:3px;width:44px;background:%s;margin-top:18px;border-radius:2px;"></div>`, accent)
	h.WriteString(`</td></tr><tr><td style="padding:22px 32px 8px 32px;font-size:16px;line-height:1.6;">`)
	for _, para := range strings.Split(body, "\n\n") {
		if para = strings.TrimSpace(para); para != "" {
			fmt.Fprintf(&h, `<p style="margin:0 0 16px 0;">%s</p>`, strings.ReplaceAll(e(para), "\n", "<br>"))
		}
	}
	if d.Link != "" && t.Button != "" {
		fmt.Fprintf(&h, `<p style="margin:8px 0 20px 0;"><a href="%s" style="display:inline-block;background:%s;color:%s;text-decoration:none;font-weight:600;font-size:15px;padding:12px 22px;border-radius:8px;">%s</a></p>`,
			e(d.Link), accent, inkOn(accent), e(t.Button))
		fmt.Fprintf(&h, `<p style="margin:0 0 16px 0;font-size:13px;line-height:1.5;color:#52525b;">If the button does not work, open this address:<br><a href="%s" style="color:#52525b;word-break:break-all;">%s</a></p>`, e(d.Link), e(d.Link))
	}
	h.WriteString(`</td></tr><tr><td style="padding:16px 32px 26px 32px;border-top:1px solid #e4e4e7;font-size:13px;line-height:1.5;color:#52525b;">`)
	h.WriteString(e(footer))
	if b.SupportEmail != "" {
		fmt.Fprintf(&h, `<br><a href="mailto:%s" style="color:#52525b;">%s</a>`, e(b.SupportEmail), e(b.SupportEmail))
	}
	h.WriteString(`</td></tr></table></td></tr></table></body></html>`)

	return RenderedEmail{Subject: subject, Text: text, HTML: h.String()}
}
