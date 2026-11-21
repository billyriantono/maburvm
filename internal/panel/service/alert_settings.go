package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/maburvm/panel/internal/shared/models"
)

// AlertSettingsSection is the system_settings row operator alerts are kept in.
const AlertSettingsSection = "alerts"

const (
	DefaultAlertCooldownMinutes  = 10
	DefaultStorageCriticalPct    = 95
	DefaultNodeOfflineAfterPolls = 3

	AlertWebhookGeneric = "generic"
	AlertWebhookSlack   = "slack"
	AlertWebhookDiscord = "discord"
)

// AlertKinds are the switches an operator sees. A node that is reachable but
// has stopped reporting goes with "node offline".
var AlertKinds = []string{
	models.AlertKindNodeOffline, models.AlertKindStoragePool, models.AlertKindBackupFailed,
	models.AlertKindJobFailed, models.AlertKindFirewallOff, models.AlertKindNetworkDrift,
}

// ErrAlertSettings is a save or a test refused for what was entered.
var ErrAlertSettings = errors.New("invalid alert settings")

type storedAlertSettings struct {
	EmailEnabled    bool     `json:"email_enabled"`
	EmailRecipients []string `json:"email_recipients"`
	WebhookEnabled  bool     `json:"webhook_enabled"`
	WebhookFormat   string   `json:"webhook_format"`
	// The address is a credential for chat webhooks, so it is encrypted like
	// the signing secret.
	WebhookURLEnc    string `json:"webhook_url_enc"`
	WebhookSecretEnc string `json:"webhook_secret_enc"`
	// A kind missing here is on.
	Kinds                  map[string]bool `json:"kinds"`
	StorageCriticalPercent int             `json:"storage_critical_percent"`
	CooldownMinutes        int             `json:"cooldown_minutes"`
	NodeOfflineAfter       int             `json:"node_offline_after"`
}

// AlertSettingsInput is what the settings form sends. An empty webhook address
// or signing secret keeps the saved one.
type AlertSettingsInput struct {
	EmailEnabled           bool            `json:"email_enabled"`
	EmailRecipients        []string        `json:"email_recipients"`
	WebhookEnabled         bool            `json:"webhook_enabled"`
	WebhookFormat          string          `json:"webhook_format"`
	WebhookURL             string          `json:"webhook_url"`
	WebhookSecret          string          `json:"webhook_secret"`
	RemoveWebhook          bool            `json:"remove_webhook"`
	Kinds                  map[string]bool `json:"kinds"`
	StorageCriticalPercent int             `json:"storage_critical_percent"`
	CooldownMinutes        int             `json:"cooldown_minutes"`
	NodeOfflineAfter       int             `json:"node_offline_after"`
}

// AlertSettingsStatus is what an administrator may read back. The webhook
// address and signing secret are never part of it, only whether they are saved
// and which host the address points at.
type AlertSettingsStatus struct {
	EmailEnabled           bool            `json:"email_enabled"`
	EmailRecipients        []string        `json:"email_recipients"`
	SMTPConfigured         bool            `json:"smtp_configured"`
	WebhookEnabled         bool            `json:"webhook_enabled"`
	WebhookFormat          string          `json:"webhook_format"`
	HasWebhookURL          bool            `json:"has_webhook_url"`
	WebhookHost            string          `json:"webhook_host"`
	HasWebhookSecret       bool            `json:"has_webhook_secret"`
	Kinds                  map[string]bool `json:"kinds"`
	StorageCriticalPercent int             `json:"storage_critical_percent"`
	CooldownMinutes        int             `json:"cooldown_minutes"`
	NodeOfflineAfter       int             `json:"node_offline_after"`
}

// alertConfig is the saved settings with defaults filled in and the webhook
// address and secret decrypted, ready to act on.
type alertConfig struct {
	storedAlertSettings
	webhookURL    string
	webhookSecret string
	cooldown      time.Duration
	brand         string
	publicURL     string
}

func (c alertConfig) kindOn(kind string) bool {
	if kind == models.AlertKindNodeSilent {
		kind = models.AlertKindNodeOffline
	}
	on, set := c.Kinds[kind]
	return !set || on
}

func (c alertConfig) emailOn() bool   { return c.EmailEnabled && len(c.EmailRecipients) > 0 }
func (c alertConfig) webhookOn() bool { return c.WebhookEnabled && c.webhookURL != "" }
func (c alertConfig) anyChannel() bool {
	return c.emailOn() || c.webhookOn()
}

func (s *storedAlertSettings) fillDefaults() {
	if s.WebhookFormat == "" {
		s.WebhookFormat = AlertWebhookGeneric
	}
	if s.StorageCriticalPercent <= 0 {
		s.StorageCriticalPercent = DefaultStorageCriticalPct
	}
	if s.CooldownMinutes <= 0 {
		s.CooldownMinutes = DefaultAlertCooldownMinutes
	}
	if s.NodeOfflineAfter <= 0 {
		s.NodeOfflineAfter = DefaultNodeOfflineAfterPolls
	}
	if s.EmailRecipients == nil {
		s.EmailRecipients = []string{}
	}
}

func (s *AlertService) decrypt(enc string) string {
	if enc == "" {
		return ""
	}
	plain, err := decryptWithKey(s.key, enc)
	for _, k := range s.prevKeys {
		if err == nil {
			break
		}
		plain, err = decryptWithKey(k, enc)
	}
	if err != nil {
		return ""
	}
	return plain
}

func (s *AlertService) loadStored(ctx context.Context) storedAlertSettings {
	var raw string
	var st storedAlertSettings
	if err := s.db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", AlertSettingsSection).
		Scan(&raw).Error; err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	st.fillDefaults()
	return st
}

func (s *AlertService) resolve(ctx context.Context, st storedAlertSettings) alertConfig {
	return alertConfig{
		storedAlertSettings: st,
		webhookURL:          s.decrypt(st.WebhookURLEnc),
		webhookSecret:       s.decrypt(st.WebhookSecretEnc),
		cooldown:            time.Duration(st.CooldownMinutes) * time.Minute,
		brand:               LoadBranding(s.db.WithContext(ctx)).Name,
		publicURL:           PublicURL(),
	}
}

// config returns the settings to act on. Cached briefly: the collector asks
// several times a minute. A save drops the cache.
func (s *AlertService) config(ctx context.Context) alertConfig {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.cached != nil && time.Since(s.cachedAt) < 15*time.Second {
		return *s.cached
	}
	cfg := s.resolve(ctx, s.loadStored(ctx))
	s.cached, s.cachedAt = &cfg, time.Now()
	return cfg
}

// NodeOfflineAfter is how many checks in a row a node may miss before it is
// marked offline.
func (s *AlertService) NodeOfflineAfter(ctx context.Context) int {
	if s == nil {
		return DefaultNodeOfflineAfterPolls
	}
	return s.config(ctx).NodeOfflineAfter
}

// StorageCriticalPercent is how full a pool is when its alert turns critical.
func (s *AlertService) StorageCriticalPercent(ctx context.Context) int {
	if s == nil {
		return DefaultStorageCriticalPct
	}
	return s.config(ctx).StorageCriticalPercent
}

// Status reports the settings without the webhook address or the secret.
func (s *AlertService) Status(ctx context.Context) AlertSettingsStatus {
	st := s.loadStored(ctx)
	out := AlertSettingsStatus{
		EmailEnabled: st.EmailEnabled, EmailRecipients: st.EmailRecipients,
		WebhookEnabled: st.WebhookEnabled, WebhookFormat: st.WebhookFormat,
		HasWebhookURL: st.WebhookURLEnc != "", HasWebhookSecret: st.WebhookSecretEnc != "",
		Kinds:                  map[string]bool{},
		StorageCriticalPercent: st.StorageCriticalPercent,
		CooldownMinutes:        st.CooldownMinutes,
		NodeOfflineAfter:       st.NodeOfflineAfter,
	}
	if u, err := url.Parse(s.decrypt(st.WebhookURLEnc)); err == nil {
		out.WebhookHost = u.Host
	}
	for _, k := range AlertKinds {
		on, set := st.Kinds[k]
		out.Kinds[k] = !set || on
	}
	_, out.SMTPConfigured, _ = LoadSMTPSettings(s.db.WithContext(ctx))
	return out
}

func refuse(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrAlertSettings, fmt.Sprintf(format, a...))
}

// merge checks what the form sent and lays it over the saved settings.
func (s *AlertService) merge(ctx context.Context, in AlertSettingsInput) (storedAlertSettings, error) {
	saved := s.loadStored(ctx)
	st := storedAlertSettings{
		EmailEnabled: in.EmailEnabled, WebhookEnabled: in.WebhookEnabled,
		WebhookFormat:    strings.TrimSpace(in.WebhookFormat),
		WebhookURLEnc:    saved.WebhookURLEnc,
		WebhookSecretEnc: saved.WebhookSecretEnc,
		Kinds:            map[string]bool{},
		EmailRecipients:  []string{},
	}
	seen := map[string]bool{}
	for _, r := range in.EmailRecipients {
		if r = strings.TrimSpace(r); r == "" {
			continue
		}
		addr, err := mail.ParseAddress(r)
		if err != nil {
			return st, refuse("%q is not an email address", r)
		}
		if !seen[strings.ToLower(addr.Address)] {
			seen[strings.ToLower(addr.Address)] = true
			st.EmailRecipients = append(st.EmailRecipients, addr.Address)
		}
	}
	if len(st.EmailRecipients) > 20 {
		return st, refuse("at most 20 recipients")
	}
	if st.EmailEnabled && len(st.EmailRecipients) == 0 {
		return st, refuse("add at least one recipient to send alerts by email")
	}

	switch st.WebhookFormat {
	case "":
		st.WebhookFormat = AlertWebhookGeneric
	case AlertWebhookGeneric, AlertWebhookSlack, AlertWebhookDiscord:
	default:
		return st, refuse("unknown webhook format %q", st.WebhookFormat)
	}
	if in.RemoveWebhook {
		st.WebhookURLEnc, st.WebhookSecretEnc, st.WebhookEnabled = "", "", false
	} else {
		if raw := strings.TrimSpace(in.WebhookURL); raw != "" {
			u, err := url.Parse(raw)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return st, refuse("the webhook address must be an http or https URL")
			}
			enc, err := encryptWithKey(s.key, raw)
			if err != nil {
				return st, fmt.Errorf("could not encrypt the webhook address: %w", err)
			}
			st.WebhookURLEnc = enc
		}
		if in.WebhookSecret != "" {
			enc, err := encryptWithKey(s.key, in.WebhookSecret)
			if err != nil {
				return st, fmt.Errorf("could not encrypt the signing secret: %w", err)
			}
			st.WebhookSecretEnc = enc
		}
	}
	if st.WebhookEnabled && s.decrypt(st.WebhookURLEnc) == "" {
		return st, refuse("enter the webhook address to send alerts to a webhook")
	}

	for _, k := range AlertKinds {
		if on, set := in.Kinds[k]; set {
			st.Kinds[k] = on
		} else if on, set := saved.Kinds[k]; set {
			st.Kinds[k] = on
		}
	}
	var err error
	if st.StorageCriticalPercent, err = within(in.StorageCriticalPercent, saved.StorageCriticalPercent, 50, 100, "the critical storage level"); err != nil {
		return st, err
	}
	if st.CooldownMinutes, err = within(in.CooldownMinutes, saved.CooldownMinutes, 1, 1440, "the settle time"); err != nil {
		return st, err
	}
	if st.NodeOfflineAfter, err = within(in.NodeOfflineAfter, saved.NodeOfflineAfter, 1, 30, "the missed checks before a node is offline"); err != nil {
		return st, err
	}
	return st, nil
}

// within returns v when it is in range, the saved value when v was not sent.
func within(v, saved, lo, hi int, what string) (int, error) {
	if v == 0 {
		return saved, nil
	}
	if v < lo || v > hi {
		return 0, refuse("%s must be between %d and %d", what, lo, hi)
	}
	return v, nil
}

// Save stores the settings.
func (s *AlertService) Save(ctx context.Context, in AlertSettingsInput) error {
	st, err := s.merge(ctx, in)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(st)
	if err := s.db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		AlertSettingsSection, string(data),
	).Error; err != nil {
		return err
	}
	s.cfgMu.Lock()
	s.cached = nil
	s.cfgMu.Unlock()
	return nil
}

// Test sends a test alert the way the form is set, saved or not, and says how
// each channel answered. Nothing is recorded in the history.
func (s *AlertService) Test(ctx context.Context, in AlertSettingsInput) ([]AlertChannelResult, error) {
	st, err := s.merge(ctx, in)
	if err != nil {
		return nil, err
	}
	cfg := s.resolve(ctx, st)
	if !cfg.anyChannel() {
		return nil, refuse("switch on email or the webhook first")
	}
	now := s.now()
	test := models.Alert{
		ID: "test", Key: "test", Kind: "test", Severity: models.AlertSeverityWarning, State: models.AlertStateEvent,
		Title:   "Test alert",
		Detail:  "Sent from the alert settings. If you can read this, alerts from this panel reach you here.",
		FiredAt: now,
	}
	msg := buildAlertMessage(cfg, []models.Alert{test}, nil, now)
	msg.Payload.Test = true
	return s.deliverAll(ctx, cfg, msg), nil
}

// AlertChannelResult is how one channel answered a delivery.
type AlertChannelResult struct {
	Channel string `json:"channel"` // email, webhook
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

// AlertWebhookPayload is the body of the generic webhook. The shape is a
// contract with whatever receives it: fields are added, never renamed or removed.
type AlertWebhookPayload struct {
	Version  string             `json:"version"`
	Source   string             `json:"source"`
	PanelURL string             `json:"panel_url,omitempty"`
	SentAt   time.Time          `json:"sent_at"`
	Test     bool               `json:"test,omitempty"`
	Text     string             `json:"text"`
	Alerts   []AlertWebhookItem `json:"alerts"`
}

// AlertWebhookItem is one alert in the generic webhook body.
type AlertWebhookItem struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Status   string `json:"status"` // firing, resolved, event
	Severity string `json:"severity"`
	Title    string `json:"title"`
	Detail   string `json:"detail"`
	Resource struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"resource"`
	FiredAt    time.Time  `json:"fired_at"`
	ResolvedAt *time.Time `json:"resolved_at"`
}

type alertMessage struct {
	Subject string
	Text    string
	Payload AlertWebhookPayload
}

// buildAlertMessage turns what is due into one message: a subject, a text for
// email and chat, and the structured body for the generic webhook.
func buildAlertMessage(cfg alertConfig, firing, recovered []models.Alert, now time.Time) alertMessage {
	brand := cfg.brand
	if brand == "" {
		brand = DefaultBrandName
	}
	var lines []string
	var first string
	items := make([]AlertWebhookItem, 0, len(firing)+len(recovered))
	add := func(a models.Alert, status, word, title, detail string) {
		if first == "" {
			first = title
		}
		line := word + ": " + title
		if detail != "" {
			line += "\n" + detail
		}
		lines = append(lines, line)
		it := AlertWebhookItem{
			ID: a.ID, Key: a.Key, Kind: a.Kind, Status: status, Severity: a.Severity,
			Title: title, Detail: detail, FiredAt: a.FiredAt.UTC(),
		}
		if a.ResolvedAt != nil {
			at := a.ResolvedAt.UTC()
			it.ResolvedAt = &at
		}
		it.Resource.Type, it.Resource.ID, it.Resource.Name = a.ResourceType, a.ResourceID, a.ResourceName
		items = append(items, it)
	}
	for _, a := range firing {
		word := "Warning"
		if a.Severity == models.AlertSeverityCritical {
			word = "Critical"
		}
		add(a, a.State, word, a.Title, a.Detail)
	}
	for _, a := range recovered {
		detail := ""
		if a.ResolvedAt != nil {
			detail = "Clear again after " + roundAge(a.ResolvedAt.Sub(a.FiredAt)) + "."
		}
		add(a, models.AlertStateResolved, "Resolved", a.Title, detail)
	}
	subject := first
	if n := len(items); n > 1 {
		subject = fmt.Sprintf("%d alerts: %s and %d more", n, first, n-1)
	}
	text := strings.Join(lines, "\n\n")
	return alertMessage{
		Subject: "[" + brand + "] " + subject,
		Text:    text,
		Payload: AlertWebhookPayload{
			Version: "1", Source: brand, PanelURL: cfg.publicURL, SentAt: now.UTC(), Text: text, Alerts: items,
		},
	}
}

// webhookBody is the request body for the chosen format. Chat services take
// only a line of text, under their own field name.
func webhookBody(format string, msg alertMessage) ([]byte, error) {
	text := msg.Subject + "\n" + msg.Text
	switch format {
	case AlertWebhookSlack:
		return json.Marshal(map[string]string{"text": text})
	case AlertWebhookDiscord:
		if r := []rune(text); len(r) > 1900 { // the service refuses more than 2000
			text = string(r[:1900]) + "…"
		}
		return json.Marshal(map[string]string{"content": text})
	}
	return json.Marshal(msg.Payload)
}

func (s *AlertService) deliverAll(ctx context.Context, cfg alertConfig, msg alertMessage) []AlertChannelResult {
	var out []AlertChannelResult
	if cfg.webhookOn() {
		out = append(out, s.postWebhook(ctx, cfg, msg))
	}
	if cfg.emailOn() {
		out = append(out, s.sendEmail(ctx, cfg, msg))
	}
	return out
}

func (s *AlertService) postWebhook(ctx context.Context, cfg alertConfig, msg alertMessage) AlertChannelResult {
	res := AlertChannelResult{Channel: "webhook"}
	body, err := webhookBody(cfg.WebhookFormat, msg)
	if err != nil {
		res.Message = err.Error()
		return res
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.webhookURL, bytes.NewReader(body))
	if err != nil {
		res.Message = "the saved address is not a valid URL"
		return res
	}
	req.Header.Set("Content-Type", "application/json")
	if cfg.webhookSecret != "" {
		mac := hmac.New(sha256.New, []byte(cfg.webhookSecret))
		mac.Write(body)
		req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.http.Do(req)
	if err != nil {
		// The address is a credential: say why, never where.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		res.Message = "not reached: " + err.Error()
		return res
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		res.Message = "the receiver answered " + resp.Status
		return res
	}
	res.OK, res.Message = true, "delivered"
	return res
}

func (s *AlertService) sendEmail(ctx context.Context, cfg alertConfig, msg alertMessage) AlertChannelResult {
	res := AlertChannelResult{Channel: "email"}
	smtpCfg, ok, err := LoadSMTPSettings(s.db.WithContext(ctx))
	if err != nil {
		res.Message = err.Error()
		return res
	}
	if !ok {
		res.Message = "no mail server is set up (Settings, System, Email)"
		return res
	}
	link := ""
	if cfg.publicURL != "" {
		link = cfg.publicURL + "/alerts"
	}
	rendered := RenderEmail(EmailTemplate{Subject: msg.Subject, Body: msg.Text, Button: "Open alerts"}, smtpCfg.Brand,
		EmailData{Link: link, LogoURL: EmailLogoURL(smtpCfg.Brand, cfg.publicURL)})
	// The subject is ours, and a hostname in it may hold braces.
	rendered.Subject = oneLine(msg.Subject)

	sent := 0
	// net/smtp has no deadline of its own, and a mail server that accepts the
	// connection and then says nothing must not hold alerts up for good.
	done := make(chan error, 1)
	go func() {
		var firstErr error
		for _, to := range cfg.EmailRecipients {
			if err := SendMail(smtpCfg, to, rendered); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			sent++
		}
		done <- firstErr
	}()
	select {
	case err = <-done:
	case <-time.After(60 * time.Second):
		res.Message = "the mail server did not answer within a minute"
		return res
	case <-ctx.Done():
		res.Message = "cancelled"
		return res
	}
	if sent == 0 {
		res.Message = err.Error()
		return res
	}
	res.OK = true
	res.Message = fmt.Sprintf("sent to %d of %d", sent, len(cfg.EmailRecipients))
	if err != nil {
		res.Message += "; " + err.Error()
	}
	return res
}
