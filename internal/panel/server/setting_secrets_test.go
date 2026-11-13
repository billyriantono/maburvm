package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingSecretsNeverLeaveAndSurviveBlankSaves(t *testing.T) {
	stored := json.RawMessage(`{"smtpHost":"mail.example","smtpPassword":"hunter2"}`)

	masked := string(maskSettingSecrets(stored))
	if strings.Contains(masked, "hunter2") || !strings.Contains(masked, `"smtpPasswordSet":true`) {
		t.Fatalf("mask leaked or lost the marker: %s", masked)
	}

	// The form saves what it was given back: blank password, plus the marker.
	var kept map[string]interface{}
	_ = json.Unmarshal(keepSettingSecrets(json.RawMessage(masked), stored), &kept)
	if kept["smtpPassword"] != "hunter2" || kept["smtpPasswordSet"] != nil {
		t.Fatalf("blank save did not keep the stored secret cleanly: %v", kept)
	}

	_ = json.Unmarshal(keepSettingSecrets(json.RawMessage(`{"smtpPassword":"new"}`), stored), &kept)
	if kept["smtpPassword"] != "new" {
		t.Fatalf("a new secret must replace the stored one: %v", kept)
	}
}
