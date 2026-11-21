package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// RescueSettingsSection is the system_settings row holding the rescue image.
const RescueSettingsSection = "rescue"

// rescueISOEnv is the environment variable read when no rescue image is saved.
const rescueISOEnv = "RESCUE_ISO_URL"

// ErrNoRescueImage is what anyone asking for rescue mode is told when the
// operator has not chosen a rescue image.
var ErrNoRescueImage = errors.New("rescue mode is not available yet: no rescue image is set. An administrator chooses one under System settings, Rescue")

// RescueSettings is the saved rescue images: an ISO (an http(s) URL the node
// downloads, or the path of an ISO already on every node) and the built-in
// rescue system, a small cloud disk image the node copies for each rescue and
// sets up with the VM's address and a one-time password.
type RescueSettings struct {
	ISOURL string `json:"isoUrl"`
	// ImageURL is the rescue system's disk image (qcow2 with cloud-init or
	// systemd-networkd, such as a minimal cloud image); ImageSHA256 what it
	// must hash to.
	ImageURL    string `json:"imageUrl,omitempty"`
	ImageSHA256 string `json:"imageSha256,omitempty"`
}

// Normalize trims the images and says what is wrong with them. An empty ISO
// is allowed: it means "use RESCUE_ISO_URL, if set".
func (r *RescueSettings) Normalize() error {
	r.ISOURL = strings.TrimSpace(r.ISOURL)
	r.ImageURL, r.ImageSHA256 = strings.TrimSpace(r.ImageURL), strings.ToLower(strings.TrimSpace(r.ImageSHA256))
	if err := checkRescueImage(r.ImageURL, r.ImageSHA256); err != nil {
		return err
	}
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

// checkRescueImage checks the built-in rescue system's image: a URL the
// nodes download (held to its sha256) or a path on every node.
func checkRescueImage(ref, sum string) error {
	if ref == "" {
		if sum != "" {
			return fmt.Errorf("a checksum without a rescue system image")
		}
		return nil
	}
	if len(ref) > 2048 {
		return fmt.Errorf("the rescue system image address is longer than 2048 characters")
	}
	if sum != "" && !models.ValidSHA256(sum) {
		return fmt.Errorf("the rescue system image checksum must be a sha256 in hex")
	}
	if strings.HasPrefix(ref, "/") {
		if strings.Contains(ref, "..") {
			return fmt.Errorf("the rescue system image path must not contain ..")
		}
		return nil
	}
	u, err := url.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("the rescue system image must be an http(s) URL or the full path of an image on the nodes")
	}
	if sum == "" {
		return fmt.Errorf("give the rescue system image's sha256, so the nodes refuse a corrupt or changed download")
	}
	return nil
}

// RescueImage is the built-in rescue system's image and its checksum; empty
// when none is set.
func RescueImage(ctx context.Context, db *gorm.DB) (ref, sha256 string) {
	if db == nil {
		return "", ""
	}
	var raw string
	if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", RescueSettingsSection).Scan(&raw).Error; err != nil || raw == "" {
		return "", ""
	}
	var r RescueSettings
	if json.Unmarshal([]byte(raw), &r) != nil {
		return "", ""
	}
	return strings.TrimSpace(r.ImageURL), strings.TrimSpace(r.ImageSHA256)
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
