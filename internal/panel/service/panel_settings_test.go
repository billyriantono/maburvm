package service

import (
	"testing"
)

func setPanelSaved(t *testing.T, values map[string]string, unreadable map[string]bool) {
	t.Helper()
	panelSaved.Lock()
	panelSaved.values, panelSaved.unreadable = values, unreadable
	panelSaved.Unlock()
	t.Cleanup(func() {
		panelSaved.Lock()
		panelSaved.values, panelSaved.unreadable = nil, nil
		panelSaved.Unlock()
	})
}

func TestPanelValuePrecedence(t *testing.T) {
	t.Setenv("PANEL_PUBLIC_URL", " https://env.example.com/ ")
	t.Setenv("BILLING_API_KEY", "")
	t.Setenv("OIDC_BUTTON_LABEL", "")

	if v, src := PanelValue("publicUrl"); v != "https://env.example.com/" || src != PanelFromEnvironment {
		t.Fatalf("environment: %q %q", v, src)
	}
	if got := PublicURL(); got != "https://env.example.com" {
		t.Fatalf("PublicURL trims the slash: %q", got)
	}
	if v, src := PanelValue("oidcButtonLabel"); v != "Sign in with SSO" || src != PanelFromDefault {
		t.Fatalf("default: %q %q", v, src)
	}
	if v, src := PanelValue("billingApiKey"); v != "" || src != "" {
		t.Fatalf("unset: %q %q", v, src)
	}

	// Saved here wins over the environment; an unreadable saved secret is
	// empty rather than the environment's.
	t.Setenv("BILLING_API_KEY", "from-the-environment-0123456789")
	setPanelSaved(t, map[string]string{"publicUrl": "https://saved.example.com", "billingApiKey": ""}, map[string]bool{"billingApiKey": true})
	if got := PublicURL(); got != "https://saved.example.com" {
		t.Fatalf("saved: %q", got)
	}
	if v, src := PanelValue("billingApiKey"); v != "" || src != PanelFromSetting {
		t.Fatalf("unreadable secret must fail closed: %q %q", v, src)
	}

	for _, st := range PanelSettingsStatus() {
		if st.Secret && st.Value != "" {
			t.Fatalf("%s: a secret's value must never be reported", st.Key)
		}
		if st.Key == "billingApiKey" && (!st.Unreadable || st.Set) {
			t.Fatalf("status: %+v", st)
		}
	}
}

func TestValidatePanelSetting(t *testing.T) {
	ok := map[string][2]string{
		"publicUrl":       {" https://panel.example.com/ ", "https://panel.example.com"},
		"oidcIssuer":      {"https://idp.example.com/realms/x/", "https://idp.example.com/realms/x"},
		"billingApiKey":   {"abcdefghijklmnopqrstuvwxyz", "abcdefghijklmnopqrstuvwxyz"},
		"metricsToken":    {"0123456789abcdef", "0123456789abcdef"},
		"oidcButtonLabel": {"Sign in with the office", "Sign in with the office"},
	}
	for key, c := range ok {
		got, err := ValidatePanelSetting(key, c[0])
		if err != nil || got != c[1] {
			t.Fatalf("%s %q: %q %v", key, c[0], got, err)
		}
	}
	bad := map[string]string{
		"publicUrl":            "https://panel.example.com/panel",
		"oidcIssuer":           "ftp://idp.example.com",
		"billingWebhookSecret": "short",
		"metricsToken":         "has a space in it ok",
		"oidcButtonLabel":      "A label that is far too long to fit on the button",
		"notASetting":          "x",
	}
	for key, v := range bad {
		if _, err := ValidatePanelSetting(key, v); err == nil {
			t.Fatalf("%s %q must be refused", key, v)
		}
	}
	if _, err := ValidatePanelSetting("publicUrl", "https://user:pw@panel.example.com"); err == nil {
		t.Fatal("credentials in the panel address must be refused")
	}
}
