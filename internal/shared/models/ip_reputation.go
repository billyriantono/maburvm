package models

import "time"

// AbuseScoreNotChecked marks a reputation record whose abuse score was never
// obtained — no API key configured, or the lookup failed.
//
// Distinct from zero on purpose. Zero means "checked, and clean"; displaying an
// unchecked address as zero is how an operator concludes their space is fine
// while their customers' mail is bouncing.
const AbuseScoreNotChecked = -1

// IPReputation is what the outside world currently thinks of one of our
// addresses.
//
// It exists because an address keeps its reputation after the abuse that earned
// it has stopped, and nothing in the panel could see that: an address used to
// scan the internet at 90k packets/sec was handed to a paying customer days
// later, who inherits the mail rejections and CAPTCHA challenges with no
// explanation available anywhere in the product.
type IPReputation struct {
	ID      uint64 `json:"id" gorm:"column:id;primaryKey;autoIncrement"`
	Address string `json:"address" gorm:"column:address;type:inet;not null;uniqueIndex"`
	PoolID  string `json:"pool_id" gorm:"column:pool_id;type:uuid"`

	// Listings names the blocklists that answered "listed". Stored as JSON
	// rather than a Postgres text[] so no driver-specific array type is needed —
	// the codebase already serialises slices this way.
	Listings []string `json:"listings" gorm:"column:listings;type:jsonb;serializer:json"`

	AbuseScore     int        `json:"abuse_score" gorm:"column:abuse_score;not null;default:-1"`
	TotalReports   int        `json:"total_reports" gorm:"column:total_reports;not null;default:0"`
	LastReportedAt *time.Time `json:"last_reported_at" gorm:"column:last_reported_at"`

	// CheckError explains why a check could not be completed. A blocklist that
	// refuses a query does not answer "unlisted", and recording a refusal as a
	// clean result would be worse than not checking at all.
	CheckError string    `json:"check_error" gorm:"column:check_error;not null;default:''"`
	CheckedAt  time.Time `json:"checked_at" gorm:"column:checked_at;not null"`

	// Filled for display, not columns.
	PoolName   string `json:"pool_name,omitempty" gorm:"-"`
	VMHostname string `json:"vm_hostname,omitempty" gorm:"-"`
	Assigned   bool   `json:"assigned" gorm:"-"`
}

func (IPReputation) TableName() string { return "ip_reputation" }

// Flagged reports whether this address needs an operator's attention.
func (r IPReputation) Flagged() bool {
	return len(r.Listings) > 0 || r.AbuseScore > 0
}
