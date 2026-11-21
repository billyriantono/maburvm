package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// PortalSettingsSection is the system_settings row holding what the customer
// area offers beyond the panel's defaults. Saved through the generic PUT
// /settings/system, which runs ValidatePortalSettings on it.
const PortalSettingsSection = "portal"

// How customers' requests for another address are handled.
const (
	IPRequestsOff      = "off"      // customers cannot ask
	IPRequestsApproval = "approval" // staff decide each one (the default)
	IPRequestsAuto     = "auto"     // granted at once, within the plan's allowance
)

// Graph history kept by default, and the most that may be asked for.
const (
	DefaultGraphHistoryDays = 90
	maxGraphHistoryDays     = 400
	maxPortalISOs           = 100
)

// PortalISO is an install or recovery image staff publish for customers to
// mount.
type PortalISO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
}

// PortalSettings is the saved section.
type PortalSettings struct {
	// Links shown in the customer area; empty hides them.
	BillingURL string `json:"billingUrl"`
	StatusURL  string `json:"statusUrl"`
	// ISOs customers may mount without typing an address.
	ISOs []PortalISO `json:"isos"`
	// ISOURLAllowed lets customers mount an ISO from an address of their own.
	// Unset = allowed, which is how the panel behaved before the list.
	ISOURLAllowed *bool `json:"isoUrlAllowed"`
	// IPRequests is IPRequests*; empty means approval.
	IPRequests string `json:"ipRequests"`
	// GraphHistoryDays is how long hourly usage averages are kept; 0 means
	// DefaultGraphHistoryDays.
	GraphHistoryDays int `json:"graphHistoryDays"`
}

func checkLink(what, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > 2048 {
		return "", fmt.Errorf("%s must start with http:// or https://", what)
	}
	return raw, nil
}

// Normalize checks the section and gives every ISO an id.
func (p *PortalSettings) Normalize() error {
	var err error
	if p.BillingURL, err = checkLink("The billing link", p.BillingURL); err != nil {
		return err
	}
	if p.StatusURL, err = checkLink("The status page link", p.StatusURL); err != nil {
		return err
	}
	switch p.IPRequests {
	case "", IPRequestsOff, IPRequestsApproval, IPRequestsAuto:
	default:
		return fmt.Errorf("address requests must be off, approval or auto")
	}
	if p.GraphHistoryDays < 0 || p.GraphHistoryDays > maxGraphHistoryDays {
		return fmt.Errorf("graph history must be between 8 and %d days", maxGraphHistoryDays)
	}
	if p.GraphHistoryDays != 0 && p.GraphHistoryDays < 8 {
		return fmt.Errorf("graph history must be between 8 and %d days: the last 7 are kept in full anyway", maxGraphHistoryDays)
	}
	if len(p.ISOs) > maxPortalISOs {
		return fmt.Errorf("at most %d ISOs can be listed", maxPortalISOs)
	}
	seen := map[string]bool{}
	for i := range p.ISOs {
		iso := &p.ISOs[i]
		iso.Name, iso.Description = strings.TrimSpace(iso.Name), strings.TrimSpace(iso.Description)
		if iso.Name == "" || len(iso.Name) > 100 || len(iso.Description) > 300 {
			return fmt.Errorf("every ISO needs a name of up to 100 characters, and a description of up to 300")
		}
		if iso.URL, err = checkLink("The address of "+iso.Name, iso.URL); err != nil || iso.URL == "" {
			if err == nil {
				err = fmt.Errorf("%s needs an http(s) address", iso.Name)
			}
			return err
		}
		if _, perr := uuid.Parse(iso.ID); perr != nil || seen[iso.ID] {
			iso.ID = uuid.New().String()
		}
		seen[iso.ID] = true
	}
	if p.ISOs == nil {
		p.ISOs = []PortalISO{}
	}
	return nil
}

// ValidatePortalSettings checks a section about to be saved.
func ValidatePortalSettings(data []byte) ([]byte, error) {
	var p PortalSettings
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("invalid customer area settings")
	}
	if err := p.Normalize(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}

// LoadPortalSettings reads the section; a missing or unreadable row is the
// defaults.
func LoadPortalSettings(ctx context.Context, db *gorm.DB) PortalSettings {
	var p PortalSettings
	if db != nil {
		var raw string
		if err := db.WithContext(ctx).Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", PortalSettingsSection).
			Scan(&raw).Error; err == nil && raw != "" {
			_ = json.Unmarshal([]byte(raw), &p)
		}
	}
	if p.ISOs == nil {
		p.ISOs = []PortalISO{}
	}
	return p
}

// ISOURLsAllowed reports whether customers may mount an ISO from their own
// address.
func (p PortalSettings) ISOURLsAllowed() bool { return p.ISOURLAllowed == nil || *p.ISOURLAllowed }

// IPRequestMode is how address requests are handled.
func (p PortalSettings) IPRequestMode() string {
	if p.IPRequests == "" {
		return IPRequestsApproval
	}
	return p.IPRequests
}

// GraphDays is how many days of hourly averages are kept.
func (p PortalSettings) GraphDays() int {
	if p.GraphHistoryDays == 0 {
		return DefaultGraphHistoryDays
	}
	return p.GraphHistoryDays
}

// ISO finds a published ISO by id.
func (p PortalSettings) ISO(id string) (PortalISO, bool) {
	for _, iso := range p.ISOs {
		if iso.ID == id {
			return iso, true
		}
	}
	return PortalISO{}, false
}

// CustomerPortal is what the customer area needs to know: its links, the
// ISOs on offer and what customers may ask for.
type CustomerPortal struct {
	SupportURL    string        `json:"support_url"`
	SupportEmail  string        `json:"support_email"`
	BillingURL    string        `json:"billing_url"`
	StatusURL     string        `json:"status_url"`
	ISOs          []CustomerISO `json:"isos"`
	ISOURLAllowed bool          `json:"iso_url_allowed"`
	IPRequests    string        `json:"ip_requests"`
	GraphDays     int           `json:"graph_days"`
}

// CustomerISO is a published ISO as customers see it.
type CustomerISO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

// LoadCustomerPortal assembles the customer area's settings.
func LoadCustomerPortal(ctx context.Context, db *gorm.DB) CustomerPortal {
	p := LoadPortalSettings(ctx, db)
	b := LoadBranding(db.WithContext(ctx))
	out := CustomerPortal{SupportURL: b.SupportURL, SupportEmail: b.SupportEmail, BillingURL: p.BillingURL, StatusURL: p.StatusURL,
		ISOs: []CustomerISO{}, ISOURLAllowed: p.ISOURLsAllowed(), IPRequests: p.IPRequestMode(), GraphDays: p.GraphDays()}
	for _, iso := range p.ISOs {
		out.ISOs = append(out.ISOs, CustomerISO{ID: iso.ID, Name: iso.Name, Description: iso.Description})
	}
	return out
}
