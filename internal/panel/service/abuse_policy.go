package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// SettingsSectionAbuse holds what the panel watches guests for and what it
// does on its own when one goes over.
const SettingsSectionAbuse = "abuse"

// AbuseRule is one reading watched: per second, and what is done once a guest
// stays over it.
type AbuseRule struct {
	Enabled   bool    `json:"enabled"`
	Threshold float64 `json:"threshold"`
	Action    string  `json:"action"`
}

// AbusePolicy is the abuse section. Every automatic action is off until staff
// choose one: out of the box the panel only watches and tells staff.
type AbusePolicy struct {
	OutboundSYN AbuseRule `json:"outbound_syn"`
	OutboundPPS AbuseRule `json:"outbound_pps"`
	InboundSYN  AbuseRule `json:"inbound_syn"`
	InboundPPS  AbuseRule `json:"inbound_pps"`
	// SustainSamples is how many readings in a row (one a minute) must be over
	// a threshold before its action is taken, so one burst never cuts anyone off.
	SustainSamples int `json:"sustain_samples"`
	// NullRouteMinutes is how long an automatic null-route lasts.
	NullRouteMinutes int `json:"null_route_minutes"`
	// RateLimitMbps is the speed an automatic rate limit holds a VM to.
	RateLimitMbps int `json:"rate_limit_mbps"`
	// NotifyOwner emails a VM's owner when an action is taken on it or lifted.
	NotifyOwner bool `json:"notify_owner"`
}

// DefaultAbusePolicy watches every reading and acts on none. The outbound
// connection threshold is the node's own rate limit; the others sit well above
// what a busy server does (a 1 Gbit/s link of full-size packets is about
// 83,000 packets a second).
func DefaultAbusePolicy() AbusePolicy {
	return AbusePolicy{
		OutboundSYN:      AbuseRule{Enabled: true, Threshold: models.AbuseSYNRateThreshold, Action: models.AbuseActionAlert},
		OutboundPPS:      AbuseRule{Enabled: true, Threshold: 150_000, Action: models.AbuseActionAlert},
		InboundSYN:       AbuseRule{Enabled: true, Threshold: 20_000, Action: models.AbuseActionAlert},
		InboundPPS:       AbuseRule{Enabled: true, Threshold: 200_000, Action: models.AbuseActionAlert},
		SustainSamples:   2,
		NullRouteMinutes: 30,
		RateLimitMbps:    10,
		NotifyOwner:      true,
	}
}

// Rule returns the rule of one reading.
func (p AbusePolicy) Rule(kind string) AbuseRule {
	switch kind {
	case models.AbuseOutboundSYN:
		return p.OutboundSYN
	case models.AbuseOutboundPPS:
		return p.OutboundPPS
	case models.AbuseInboundSYN:
		return p.InboundSYN
	case models.AbuseInboundPPS:
		return p.InboundPPS
	}
	return AbuseRule{}
}

// abuseActionsFor are the actions that make sense for a reading: a guest
// sending too much is quarantined or slowed, one receiving too much is
// null-routed to protect its neighbours.
func abuseActionsFor(kind string) []string {
	if kind == models.AbuseInboundSYN || kind == models.AbuseInboundPPS {
		return []string{models.AbuseActionAlert, models.AbuseActionNullRoute}
	}
	return []string{models.AbuseActionAlert, models.AbuseActionRateLimit, models.AbuseActionQuarantine}
}

// Validate refuses a policy that cannot be acted on.
func (p *AbusePolicy) Validate() error {
	for _, kind := range models.AbuseKinds {
		r := p.Rule(kind)
		if r.Threshold <= 0 {
			return fmt.Errorf("%s: the threshold must be above zero", kind)
		}
		ok := false
		for _, a := range abuseActionsFor(kind) {
			ok = ok || a == r.Action
		}
		if !ok {
			return fmt.Errorf("%s: the action must be one of %v", kind, abuseActionsFor(kind))
		}
	}
	if p.SustainSamples < 1 || p.SustainSamples > 30 {
		return fmt.Errorf("sustain_samples must be between 1 and 30")
	}
	if p.NullRouteMinutes < 1 || p.NullRouteMinutes > 7*24*60 {
		return fmt.Errorf("null_route_minutes must be between 1 and %d", 7*24*60)
	}
	if p.RateLimitMbps < 1 || p.RateLimitMbps > 100_000 {
		return fmt.Errorf("rate_limit_mbps must be between 1 and 100000")
	}
	return nil
}

// LoadAbusePolicy reads the section over the defaults, so a field a saved
// policy predates keeps its default.
func LoadAbusePolicy(ctx context.Context, db *gorm.DB) AbusePolicy {
	p := DefaultAbusePolicy()
	if db == nil {
		return p
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", SettingsSectionAbuse).
		Scan(&raw).Error; err != nil || raw == "" {
		return p
	}
	saved := p
	if json.Unmarshal([]byte(raw), &saved) != nil || saved.Validate() != nil {
		return p
	}
	return saved
}

// SaveAbusePolicy stores the section.
func SaveAbusePolicy(ctx context.Context, db *gorm.DB, p AbusePolicy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	data, _ := json.Marshal(p)
	return db.WithContext(ctx).Exec(
		"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
			"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
		SettingsSectionAbuse, string(data),
	).Error
}
