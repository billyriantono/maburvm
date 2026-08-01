package models

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// DiskQuotaReservation is a durable, pending extra-disk admission reservation.
//
// When a user requests an extra data disk, the service reserves the disk
// capacity BEFORE driving the agent AttachDisk RPC. The reservation is counted
// against the user's disk quota (alongside boot disks, active vm_disks, and any
// other pending reservations) so a concurrent increase cannot double-spend the
// same capacity. After the agent successfully attaches the disk, the reservation
// is atomically consumed and the corresponding vm_disks row is created; on agent
// failure the reservation is released.
//
// Lifecycle (DB-enforced, see migration 040a):
//   - status is only 'pending' or 'consumed';
//   - pending  => consumed_at IS NULL;
//   - consumed => consumed_at IS NOT NULL;
//   - at most ONE pending reservation may exist per VM (partial unique index).
//
// RELEASE IS HARD (physical DELETE), NOT soft deletion. This model therefore has
// NO gorm.DeletedAt field: any GORM Delete on a reservation row physically
// removes it. There is NO TTL / automatic expiry; a pending reservation
// intentionally overcounts rather than permit an agent-attached disk to bypass
// quota. Only a final DB-recording failure AFTER agent success retains the
// reservation fail-closed (handled by the application layer), so capacity is
// never leaked.
//
// Reservation ownership is coherent: (vm_id, user_id) is a composite FK to
// vms(id, user_id) ON DELETE CASCADE, so a reservation's user always equals its
// VM's owner and a VM deletion cleans up its reservations atomically.
//
// Agent rollback / reconciliation remains Phase 2/3 debt and is NOT solved here.
type DiskQuotaReservation struct {
	ID         string     `json:"id" gorm:"type:uuid;primaryKey;default:gen_random_uuid()"`
	UserID     string     `json:"user_id" gorm:"type:uuid;not null;index"`
	VMID       string     `json:"vm_id" gorm:"type:uuid;not null;index"`
	SizeGB     int        `json:"size_gb" gorm:"not null;check:size_gb > 0"` // strictly positive
	Status     string     `json:"status" gorm:"type:varchar(16);not null;default:'pending';check:status in ('pending','consumed')"`
	CreatedAt  time.Time  `json:"created_at" gorm:"not null;default:NOW()"`
	UpdatedAt  time.Time  `json:"updated_at" gorm:"not null;default:NOW()"`
	ConsumedAt *time.Time `json:"consumed_at"`
}

// TableName specifies the table name for DiskQuotaReservation.
func (DiskQuotaReservation) TableName() string { return "disk_quota_reservations" }

// DiskQuotaReservationStatus enumerates the reservation lifecycle states.
const (
	DiskQuotaReservationPending  = "pending"
	DiskQuotaReservationConsumed = "consumed"
)

// BeforeCreate stamps a fresh UUID when the caller did not supply one.
func (r *DiskQuotaReservation) BeforeCreate(tx *gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.New().String()
	}
	return nil
}
