package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// PanelSettingsSection holds panel-wide settings that used to live only in the
// panel's environment. Each one falls back to its variable while nothing is
// saved, so an install configured through .env.production keeps working.
// Managed only through SavePanelSettings: the generic settings endpoints
// neither return nor accept it.
const PanelSettingsSection = "panel"

// PanelSetting is one setting and the environment variable it takes over from.
// A secret is stored encrypted and never sent back to a browser.
type PanelSetting struct {
	Key     string `json:"key"`
	Env     string `json:"env"`
	Secret  bool   `json:"secret"`
	Default string `json:"default,omitempty"`
}

// PanelSettings lists every setting in the section, in the order the page shows them.
var PanelSettings = []PanelSetting{
	{Key: "publicUrl", Env: "PANEL_PUBLIC_URL"},
	{Key: "billingApiKey", Env: "BILLING_API_KEY", Secret: true},
	{Key: "billingWebhookSecret", Env: "BILLING_WEBHOOK_SECRET", Secret: true},
	{Key: "metricsToken", Env: "METRICS_TOKEN", Secret: true},
	{Key: "oidcIssuer", Env: "OIDC_ISSUER"},
	{Key: "oidcClientId", Env: "OIDC_CLIENT_ID"},
	{Key: "oidcClientSecret", Env: "OIDC_CLIENT_SECRET", Secret: true},
	{Key: "oidcButtonLabel", Env: "OIDC_BUTTON_LABEL", Default: "Sign in with SSO"},
}

// Where a panel setting's value comes from.
const (
	PanelFromSetting     = "setting"
	PanelFromEnvironment = "environment"
	PanelFromDefault     = "default"
)

// panelSaved is the decrypted section, read at boot and after every save, so
// a change applies at once without the database being read on each request.
// ponytail: per-process; a second panel process would need a restart to see a change.
var panelSaved struct {
	sync.RWMutex
	values     map[string]string
	unreadable map[string]bool
}

func panelSetting(key string) (PanelSetting, bool) {
	for _, s := range PanelSettings {
		if s.Key == key {
			return s, true
		}
	}
	return PanelSetting{}, false
}

// LoadPanelSettings reads the saved section into memory. A missing row means
// every setting comes from the environment.
func LoadPanelSettings(ctx context.Context, db *gorm.DB) error {
	stored, err := readPanelSection(ctx, db)
	if err != nil {
		return err
	}
	values, unreadable := map[string]string{}, map[string]bool{}
	for k, v := range stored {
		s, ok := panelSetting(k)
		if !ok || v == "" {
			continue
		}
		if s.Secret {
			plain, err := models.OpenSecret(v)
			if err != nil {
				// Fail closed: a credential nobody can read must not fall back
				// to the environment's, which may belong to something else.
				slog.Default().Error("a saved panel setting could not be decrypted; it is treated as empty until saved again", "setting", k, "error", err)
				unreadable[k] = true
				values[k] = ""
				continue
			}
			v = plain
		}
		values[k] = v
	}
	panelSaved.Lock()
	panelSaved.values, panelSaved.unreadable = values, unreadable
	panelSaved.Unlock()
	return nil
}

func readPanelSection(ctx context.Context, db *gorm.DB) (map[string]string, error) {
	out := map[string]string{}
	if db == nil {
		return out, nil
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", PanelSettingsSection).
		Scan(&raw).Error; err != nil {
		return nil, err
	}
	if raw == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("the saved panel settings are unreadable: %w", err)
	}
	return out, nil
}

// PanelValue is the value of a panel setting in force now and where it came
// from: saved here, the environment, the built-in default, or "" for none.
func PanelValue(key string) (string, string) {
	panelSaved.RLock()
	v, saved := panelSaved.values[key]
	panelSaved.RUnlock()
	if saved {
		return v, PanelFromSetting
	}
	s, _ := panelSetting(key)
	if s.Env != "" {
		if env := strings.TrimSpace(os.Getenv(s.Env)); env != "" {
			return env, PanelFromEnvironment
		}
	}
	if s.Default != "" {
		return s.Default, PanelFromDefault
	}
	return "", ""
}

// PublicURL is the panel's public address without a trailing slash, or "".
func PublicURL() string {
	v, _ := PanelValue("publicUrl")
	return strings.TrimRight(strings.TrimSpace(v), "/")
}

// PanelSettingStatus is what an administrator sees of one setting. Value is
// always empty for a secret: only whether one is set.
type PanelSettingStatus struct {
	PanelSetting
	Value      string `json:"value"`
	Set        bool   `json:"set"`
	Source     string `json:"source"`
	Unreadable bool   `json:"unreadable,omitempty"`
}

// PanelSettingsStatus reports every setting's effective value and source.
func PanelSettingsStatus() []PanelSettingStatus {
	panelSaved.RLock()
	unreadable := panelSaved.unreadable
	panelSaved.RUnlock()
	out := make([]PanelSettingStatus, 0, len(PanelSettings))
	for _, s := range PanelSettings {
		v, src := PanelValue(s.Key)
		st := PanelSettingStatus{PanelSetting: s, Set: v != "", Source: src, Unreadable: unreadable[s.Key]}
		if !s.Secret {
			st.Value = v
		}
		out = append(out, st)
	}
	return out
}

// ValidatePanelSetting checks and normalises one value about to be saved.
func ValidatePanelSetting(key, value string) (string, error) {
	if _, ok := panelSetting(key); !ok {
		return "", fmt.Errorf("%q is not a panel setting", key)
	}
	value = strings.TrimSpace(value)
	switch key {
	case "publicUrl", "oidcIssuer":
		value = strings.TrimRight(value, "/")
		u, err := url.Parse(value)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", fmt.Errorf("%s must be an http or https address such as https://panel.example.com", label(key))
		}
		if key == "publicUrl" && u.Path != "" {
			return "", fmt.Errorf("the panel address must be the site root, without a path")
		}
	case "billingApiKey", "billingWebhookSecret":
		if len(value) < 24 || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return "", fmt.Errorf("%s must be 24 to 256 characters without spaces; whoever guesses it can create and remove VMs", label(key))
		}
	case "metricsToken":
		if len(value) < 16 || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return "", fmt.Errorf("the metrics token must be 16 to 256 characters without spaces")
		}
	case "oidcClientId":
		if len(value) > 255 {
			return "", fmt.Errorf("the client ID is longer than 255 characters")
		}
	case "oidcClientSecret":
		if len(value) > 1024 {
			return "", fmt.Errorf("the client secret is longer than 1024 characters")
		}
	case "oidcButtonLabel":
		if len([]rune(value)) > 40 {
			return "", fmt.Errorf("the button label is longer than 40 characters")
		}
	}
	return value, nil
}

func label(key string) string {
	switch key {
	case "publicUrl":
		return "the panel address"
	case "oidcIssuer":
		return "the issuer"
	case "billingApiKey":
		return "the billing API key"
	case "billingWebhookSecret":
		return "the billing signing secret"
	}
	return key
}

// SavePanelSettings applies changes: a key left out stays as it is, an empty
// value removes the saved one (its environment variable applies again), and
// anything else is checked and saved. It takes effect at once.
func SavePanelSettings(ctx context.Context, db *gorm.DB, changes map[string]*string) error {
	clean := map[string]string{}
	for k, v := range changes {
		if v == nil {
			continue
		}
		if _, ok := panelSetting(k); !ok {
			return fmt.Errorf("%q is not a panel setting", k)
		}
		if strings.TrimSpace(*v) == "" {
			clean[k] = ""
			continue
		}
		norm, err := ValidatePanelSetting(k, *v)
		if err != nil {
			return err
		}
		clean[k] = norm
	}
	stored, err := readPanelSection(ctx, db)
	if err != nil {
		return err
	}
	for k, v := range clean {
		if v == "" {
			delete(stored, k)
			continue
		}
		if s, _ := panelSetting(k); s.Secret {
			if v, err = models.SealSecret(v); err != nil {
				return fmt.Errorf("could not encrypt %s: %w", k, err)
			}
		}
		stored[k] = v
	}
	data, _ := json.Marshal(stored)
	if err := db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		PanelSettingsSection, string(data),
	).Error; err != nil {
		return err
	}
	return LoadPanelSettings(ctx, db)
}
