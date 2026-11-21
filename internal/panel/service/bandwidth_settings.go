package service

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"gorm.io/gorm"
)

// SettingsSectionBandwidth holds what happens around monthly transfer quotas:
// whether going over is enforced, and when owners are warned.
const SettingsSectionBandwidth = "bandwidth"

// DefaultUsageWarnPercents are the usage levels owners are emailed at when the
// operator has not chosen any.
var DefaultUsageWarnPercents = []int{80, 100}

// BandwidthSettings is the shape stored under the 'bandwidth' section. Enforce
// unset falls back to the BANDWIDTH_ENFORCE environment variable, which is
// where this switch used to live. WarnPercents unset means the defaults; an
// empty list turns warnings off.
type BandwidthSettings struct {
	Enforce      *bool `json:"enforce"`
	WarnPercents []int `json:"warnPercents"`
}

// Where the enforcement switch took its value from.
const (
	EnforceFromSetting     = "setting"
	EnforceFromEnvironment = "environment"
	EnforceFromDefault     = "default"
)

// LoadBandwidthSettings reads the section; a missing or unreadable row is the
// zero value, which means every default.
func LoadBandwidthSettings(ctx context.Context, db *gorm.DB) BandwidthSettings {
	var out BandwidthSettings
	if db == nil {
		return out
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", SettingsSectionBandwidth).
		Scan(&raw).Error; err != nil || raw == "" {
		return out
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// BandwidthEnforcement says whether over-quota throttling and suspension are
// applied, and where that was decided.
func BandwidthEnforcement(ctx context.Context, db *gorm.DB) (bool, string) {
	if v := LoadBandwidthSettings(ctx, db).Enforce; v != nil {
		return *v, EnforceFromSetting
	}
	if env, ok := os.LookupEnv("BANDWIDTH_ENFORCE"); ok && env != "" {
		return env == "true", EnforceFromEnvironment
	}
	return false, EnforceFromDefault
}

// UsageWarnPercents are the levels owners are warned at, lowest first.
func UsageWarnPercents(ctx context.Context, db *gorm.DB) []int {
	s := LoadBandwidthSettings(ctx, db)
	if s.WarnPercents == nil {
		return DefaultUsageWarnPercents
	}
	return s.WarnPercents
}

// Normalize sorts the warning levels and refuses ones that make no sense.
func (s *BandwidthSettings) Normalize() error {
	if s.WarnPercents == nil {
		return nil
	}
	if len(s.WarnPercents) > 5 {
		return fmt.Errorf("at most 5 warning levels")
	}
	seen := map[int]bool{}
	for _, p := range s.WarnPercents {
		if p < 1 || p > 100 {
			return fmt.Errorf("a warning level must be between 1 and 100 percent, not %d", p)
		}
		if seen[p] {
			return fmt.Errorf("the warning level %d%% is listed twice", p)
		}
		seen[p] = true
	}
	sort.Ints(s.WarnPercents)
	return nil
}

// SaveBandwidthSettings stores the section.
func SaveBandwidthSettings(ctx context.Context, db *gorm.DB, s BandwidthSettings) error {
	if err := s.Normalize(); err != nil {
		return err
	}
	data, _ := json.Marshal(s)
	return db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		SettingsSectionBandwidth, string(data),
	).Error
}

// UsageWarningLevel is the level an owner should be warned at now: the highest
// one reached that is above the last warned, or 0.
func UsageWarningLevel(used, quota int64, warned int, levels []int) int {
	if quota <= 0 {
		return 0
	}
	level := 0
	for _, l := range levels {
		if l > warned && used*100 >= quota*int64(l) && l > level {
			level = l
		}
	}
	return level
}
