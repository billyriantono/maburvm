package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// OSTemplate represents an operating system template for VM provisioning
type OSTemplate struct {
	ID        string `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	Name      string `json:"name" gorm:"type:varchar(100);not null" validate:"required,max=100"`
	Version   string `json:"version" gorm:"type:varchar(50);not null" validate:"required,max=50"`
	ImagePath string `json:"image_path" gorm:"type:varchar(255);not null" validate:"required"`
	SizeBytes int64  `json:"size_bytes" gorm:"column:size_bytes;not null;default:0"`
	// OSType is the kind of system in the image (OSTypeLinux, OSTypeRouterOS).
	OSType      string `json:"os_type" gorm:"column:os_type;type:varchar(20);not null;default:linux"`
	IsActive    bool   `json:"is_active" gorm:"default:true"`
	Description string `json:"description" gorm:"type:text"`
	// Licensed marks a system that costs the operator a licence per VM. A
	// customer installs it only on a plan in PlanIDs, or keeps it where the
	// billing system or staff installed it.
	Licensed bool `json:"licensed" gorm:"column:licensed;not null;default:false"`
	// PlanIDs restricts the template to these plans. Empty means any plan.
	PlanIDs []string `json:"plan_ids" gorm:"column:plan_ids;type:jsonb;serializer:json"`
	// Kind is TemplateKindDisk (a disk image a VM is built from) or
	// TemplateKindISO (an installer or drivers disc).
	Kind string `json:"kind" gorm:"column:kind;type:varchar(8);not null;default:disk"`
	// ChecksumSHA256 is what the downloaded image must hash to; nodes refuse
	// one that does not. ChecksumURL, when set, is a SHA256SUMS-style list the
	// checksum is read from again on a refresh.
	ChecksumSHA256    string     `json:"checksum_sha256" gorm:"column:checksum_sha256;type:varchar(64);not null;default:''"`
	ChecksumURL       string     `json:"checksum_url" gorm:"column:checksum_url;type:text;not null;default:''"`
	ChecksumCheckedAt *time.Time `json:"checksum_checked_at,omitempty" gorm:"column:checksum_checked_at"`
	// AllowHTTP accepts an image address on plain http.
	AllowHTTP bool `json:"allow_http" gorm:"column:allow_http;not null;default:false"`
	// AutoRefresh has the panel read ChecksumURL every day and fetch a changed
	// image onto the nodes.
	AutoRefresh bool `json:"auto_refresh" gorm:"column:auto_refresh;not null;default:false"`
	// Firmware is FirmwareBIOS or FirmwareUEFI: how the image boots.
	Firmware string `json:"firmware" gorm:"column:firmware;type:varchar(8);not null;default:bios"`
	// SecureBoot and TPM: VMs installed from the image get Secure Boot (UEFI
	// only) and a virtual TPM, as Windows 11 needs. Read-only here, written by
	// table name.
	SecureBoot bool           `json:"secure_boot" gorm:"column:secure_boot;->"`
	TPM        bool           `json:"tpm" gorm:"column:tpm;->"`
	CreatedAt  time.Time      `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt  time.Time      `json:"updated_at" gorm:"not null;default:NOW()"`
	DeletedAt  gorm.DeletedAt `json:"-" gorm:"index"`
}

// Template kinds and boot firmware.
const (
	TemplateKindDisk = "disk"
	TemplateKindISO  = "iso"

	FirmwareBIOS = "bios"
	FirmwareUEFI = "uefi"
)

// IsISO reports an installer or drivers disc rather than a disk image.
func (t *OSTemplate) IsISO() bool { return t.Kind == TemplateKindISO }

// TableName specifies the table name for OSTemplate
func (OSTemplate) TableName() string {
	return "os_templates"
}

// BeforeCreate hook for OSTemplate
func (t *OSTemplate) BeforeCreate(tx *gorm.DB) error {
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	return nil
}

// GetID returns the UUID representation of ID
func (t *OSTemplate) GetID() uuid.UUID {
	id, _ := uuid.Parse(t.ID)
	return id
}

// Validate validates the OSTemplate struct
func (t *OSTemplate) Validate() ValidationErrors {
	return ValidateStruct(t)
}

// OfferedOn reports whether a customer may install this template on a VM of
// the given plan ("" for a VM with no plan). A restricted template needs one
// of its plans; a licensed one with no plans is never the customer's to pick.
func (t *OSTemplate) OfferedOn(planID string) bool {
	if len(t.PlanIDs) == 0 {
		return !t.Licensed
	}
	return t.planListed(planID)
}

// RestrictedFrom reports a template limited to plans that do not include this
// one. Unlike OfferedOn it lets a licensed template with no plan list through:
// that is for an order the billing system has already priced.
func (t *OSTemplate) RestrictedFrom(planID string) bool {
	return len(t.PlanIDs) > 0 && !t.planListed(planID)
}

func (t *OSTemplate) planListed(planID string) bool {
	for _, id := range t.PlanIDs {
		if planID != "" && id == planID {
			return true
		}
	}
	return false
}
