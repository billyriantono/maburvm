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

// A section holds more than its form shows. Saving the form keeps the rest.
func TestSavingSettingsKeepsFieldsTheFormDoesNotSend(t *testing.T) {
	stored := json.RawMessage(`{"panelName":"Old","defaultRegion":"r1","sessionTimeout":60,"minPasswordLength":12}`)
	var kept map[string]interface{}
	_ = json.Unmarshal(keepSettingSecrets(json.RawMessage(`{"minPasswordLength":14}`), stored), &kept)
	if kept["minPasswordLength"] != float64(14) {
		t.Fatalf("the sent value must win: %v", kept)
	}
	if kept["defaultRegion"] != "r1" || kept["sessionTimeout"] != float64(60) || kept["panelName"] != "Old" {
		t.Fatalf("fields the save did not mention were dropped: %v", kept)
	}
}

// A JSON null for a section is "nothing sent", not a panic or a wipe.
func TestSavingNullSettingsSectionKeepsWhatIsStored(t *testing.T) {
	stored := json.RawMessage(`{"smtpHost":"mail.example","smtpPassword":"hunter2"}`)
	var kept map[string]interface{}
	_ = json.Unmarshal(keepSettingSecrets(json.RawMessage(`null`), stored), &kept)
	if kept["smtpHost"] != "mail.example" || kept["smtpPassword"] != "hunter2" {
		t.Fatalf("a null save must leave the section as it was: %v", kept)
	}
}
