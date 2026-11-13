package service

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestServerReadyEmail(t *testing.T) {
	def, ok := EmailTemplateDefFor(EmailVMReady)
	require.True(t, ok)
	require.True(t, def.Optional)

	// The email names the server and how to get in, and never carries a password.
	out := RenderEmail(def.Default, Branding{Name: "Acme"}, EmailData{
		Name: "Dewi", Email: "dewi@example.com", Link: "https://panel.example.com/client/vms/1",
		Extra: map[string]string{"server": "web-01", "address": "203.0.113.24", "system": "Ubuntu 24.04 LTS", "access": AccessPasswordOnPage},
	})
	require.Equal(t, "Your server web-01 is ready", out.Subject)
	for _, want := range []string{"web-01", "203.0.113.24", "Ubuntu 24.04 LTS", "not in this email", "https://panel.example.com/client/vms/1"} {
		require.Contains(t, out.Text, want)
	}
	require.NotContains(t, out.Text, "{{")
	require.False(t, strings.Contains(strings.ToLower(AccessOwnPassword+AccessPasswordOnPage), "password:"))

	// Every server email fills all its placeholders and can be switched off.
	for _, key := range []string{EmailVMReady, EmailVMReinstalled, EmailVMRescueOn, EmailVMRescueOff} {
		d, ok := EmailTemplateDefFor(key)
		require.True(t, ok, key)
		require.True(t, d.Optional, key)
		r := RenderEmail(d.Default, Branding{Name: "Acme"}, EmailData{Name: "Dewi", Email: "dewi@example.com", Link: d.SampleLink, Extra: d.Sample})
		require.NotContains(t, r.Subject+r.Text, "{{", key)
		require.Contains(t, r.Text, "web-01", key)
	}

	// Only optional emails can be switched off.
	cfg := SMTPSettings{Templates: map[string]EmailTemplate{
		EmailVMReady:       {Subject: "s", Body: "b", Off: true},
		EmailPasswordReset: {Subject: "s", Body: "b", Off: true},
	}}
	require.False(t, cfg.Enabled(EmailVMReady))
	require.True(t, cfg.Enabled(EmailPasswordReset))
	require.True(t, SMTPSettings{}.Enabled(EmailVMReady))
}
