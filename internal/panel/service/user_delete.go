package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// What happens to an account's VMs when the account is deleted. Without a
// choice the delete is refused while the account owns any.
const (
	UserVMsReassign = "reassign"
	UserVMsDelete   = "delete"
)

var (
	// ErrReassignTarget names a reassign target that cannot own the VMs.
	ErrReassignTarget = errors.New("choose another active account to give the VMs to")
	// ErrReassignHostnames refuses a reassign that would give the new owner two VMs of the same name.
	ErrReassignHostnames = errors.New("the new owner already has VMs with these names")
)

// OwnedVM is one VM an account owns, as the delete dialog lists it.
type OwnedVM struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
}

// UserOwnedVMs lists the VMs an account owns, VMs already being deleted
// included: they still run until their node confirms.
func UserOwnedVMs(ctx context.Context, db *gorm.DB, userID string) ([]OwnedVM, error) {
	out := []OwnedVM{}
	err := db.WithContext(ctx).Table("vms").Select("id, hostname, status").
		Where("user_id = ? AND deleted_at IS NULL", userID).Order("hostname").Scan(&out).Error
	return out, err
}

// ReassignUserVMs gives every VM of one account to another, with the floating
// IPs attached to them, inside tx. Returns the VMs moved.
func ReassignUserVMs(ctx context.Context, tx *gorm.DB, fromID, toID string) ([]OwnedVM, error) {
	if toID == "" || toID == fromID {
		return nil, ErrReassignTarget
	}
	var active int64
	if err := tx.WithContext(ctx).Table("users").Where("id = ? AND deleted_at IS NULL", toID).Count(&active).Error; err != nil {
		return nil, err
	}
	if active == 0 {
		return nil, ErrReassignTarget
	}
	vms, err := UserOwnedVMs(ctx, tx, fromID)
	if err != nil || len(vms) == 0 {
		return vms, err
	}
	var clashes []string
	if err := tx.WithContext(ctx).Table("vms").
		Where("user_id = ? AND deleted_at IS NULL AND hostname IN (?)", toID,
			tx.Table("vms").Select("hostname").Where("user_id = ? AND deleted_at IS NULL", fromID)).
		Order("hostname").Pluck("hostname", &clashes).Error; err != nil {
		return nil, err
	}
	if len(clashes) > 0 {
		return nil, fmt.Errorf("%w: %s. Rename them first", ErrReassignHostnames, strings.Join(clashes, ", "))
	}
	ids := make([]string, len(vms))
	for i, v := range vms {
		ids[i] = v.ID
	}
	if err := tx.WithContext(ctx).Table("ip_addresses").
		Where("vm_id IN ? AND user_id = ? AND deleted_at IS NULL", ids, fromID).
		Update("user_id", toID).Error; err != nil {
		return nil, fmt.Errorf("move floating IPs: %w", err)
	}
	// Disk reservations reference (vm, owner); one statement moves both, so the
	// foreign key is checked once both sides agree.
	if err := tx.WithContext(ctx).Exec(`WITH moved AS (UPDATE disk_quota_reservations SET user_id = ? WHERE vm_id IN ? RETURNING id)
		UPDATE vms SET user_id = ?, updated_at = NOW() WHERE id IN ?`, toID, ids, toID, ids).Error; err != nil {
		return nil, fmt.Errorf("move VMs: %w", err)
	}
	// The account's firewall groups go with its VMs, so the new owner can
	// still change or take off what protects them.
	if err := tx.WithContext(ctx).Exec(`UPDATE firewall_groups SET user_id = ? WHERE user_id = ?`, toID, fromID).Error; err != nil {
		return nil, fmt.Errorf("move firewall groups: %w", err)
	}
	return vms, nil
}

// AuditVMOwnerChange records a VM given to another account.
func AuditVMOwnerChange(ctx context.Context, db *gorm.DB, actorID string, vm OwnedVM, fromID, toID, ip, userAgent string) {
	entry := &models.AuditLog{
		UserID: strPtrOrNil(actorID), Action: "vm.owner_change", ResourceType: "VM", ResourceID: &vm.ID,
		IPAddress: ip, UserAgent: userAgent,
		Details: map[string]any{"hostname": vm.Hostname, "from_user": fromID, "to_user": toID, "reason": "owner deleted"},
	}
	tx := db.WithContext(ctx)
	if net.ParseIP(ip) == nil {
		tx = tx.Omit("ip_address")
	}
	if err := tx.Create(entry).Error; err != nil {
		slog.Default().Error("audit: could not record a VM owner change", "vm_id", vm.ID, "error", err)
	}
}
