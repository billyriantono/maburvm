package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gorm.io/gorm"
)

// RescueSettingsSection is the system_settings row holding the rescue image.
const RescueSettingsSection = "rescue"

// rescueISOEnv is the environment variable read when no rescue image is saved.
const rescueISOEnv = "RESCUE_ISO_URL"

// ErrNoRescueImage is what anyone asking for rescue mode is told when the
// operator has not chosen a rescue image.
var ErrNoRescueImage = errors.New("rescue mode is not available yet: no rescue image is set. An administrator chooses one under System settings, Rescue")

// RescueSettings is the saved rescue image: an http(s) URL the node downloads,
// or the path of an ISO already on every node.
type RescueSettings struct {
	ISOURL string `json:"isoUrl"`
}

// Normalize trims the image and says what is wrong with it. Empty is allowed:
// it means "use RESCUE_ISO_URL, if set".
func (r *RescueSettings) Normalize() error {
	r.ISOURL = strings.TrimSpace(r.ISOURL)
	if r.ISOURL == "" {
		return nil
	}
	if len(r.ISOURL) > 2048 {
		return fmt.Errorf("the rescue image address is longer than 2048 characters")
	}
	if strings.HasPrefix(r.ISOURL, "/") {
		if strings.Contains(r.ISOURL, "..") {
			return fmt.Errorf("the rescue image path must not contain ..")
		}
		return nil
	}
	u, err := url.Parse(r.ISOURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the rescue image must be an http(s) URL or the full path of an ISO on the nodes")
	}
	return nil
}

// ValidateRescueSettings checks a section about to be saved.
func ValidateRescueSettings(data []byte) ([]byte, error) {
	var r RescueSettings
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("invalid rescue settings")
	}
	if err := r.Normalize(); err != nil {
		return nil, err
	}
	return json.Marshal(r)
}

// RescueISO is the image rescue mode boots: the one saved in System settings,
// else RESCUE_ISO_URL. source is "settings", "environment" or "" for none.
func RescueISO(ctx context.Context, db *gorm.DB) (iso, source string) {
	if db != nil {
		var raw string
		if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", RescueSettingsSection).Scan(&raw).Error; err == nil && raw != "" {
			var r RescueSettings
			if json.Unmarshal([]byte(raw), &r) == nil && strings.TrimSpace(r.ISOURL) != "" {
				return strings.TrimSpace(r.ISOURL), "settings"
			}
		}
	}
	if env := strings.TrimSpace(os.Getenv(rescueISOEnv)); env != "" {
		return env, "environment"
	}
	return "", ""
}
