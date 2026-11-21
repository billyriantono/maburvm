package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

var (
	// ErrAccountSuspended is returned to a suspended account that signs in with
	// the right credentials.
	ErrAccountSuspended = errors.New("this account is suspended; contact support")
	// ErrNotACustomerAccount is returned when asked to suspend an administrator.
	ErrNotACustomerAccount = errors.New("only customer accounts can be suspended; remove an administrator's access by changing its role or deleting it")
)

// AccountSuspension is what suspending or restoring an account did.
type AccountSuspension struct {
	User *models.User `json:"user"`
	// VMsChanged counts the VMs suspended or restored along with the account.
	VMsChanged int `json:"vms_changed"`
	// VMsFailed names the VMs that could not be powered off or started.
	VMsFailed []string `json:"vms_failed"`
}

// SetAccountSuspended suspends or restores a customer account. A suspended
// account cannot sign in, and its sessions and API keys stop at once (every
// request re-reads the account). withVMs also suspends the account's VMs, or
// on restore lifts the suspension from every VM of the account.
// ponytail: restoring with VMs also lifts a suspension billing placed on one
// service; record the cause per VM if the two need telling apart.
func (s *VMService) SetAccountSuspended(ctx context.Context, userID string, on, withVMs bool) (*AccountSuspension, error) {
	// The billing system sends whatever staff typed; Postgres errors on a non-UUID.
	if _, err := uuid.Parse(userID); err != nil {
		return nil, ErrUserNotFound
	}
	var user models.User
	if err := s.db.WithContext(ctx).First(&user, "id = ?", userID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if on && user.Role != models.RoleClient {
		return nil, ErrNotACustomerAccount
	}

	// By table name: suspended_at is read-only on the model.
	now := time.Now()
	fields := map[string]interface{}{"suspended_at": nil}
	if on {
		// Sessions from before the suspension do not come back with the account.
		fields = map[string]interface{}{"suspended_at": now, "token_revoked_at": now}
	}
	if on == user.Suspended() {
		fields = nil // already in that state; the VMs may still need doing
	}
	if fields != nil {
		if err := s.db.WithContext(ctx).Table("users").Where("id = ?", userID).Updates(fields).Error; err != nil {
			return nil, fmt.Errorf("failed to update the account: %w", err)
		}
	}
	if on && user.SuspendedAt == nil {
		user.SuspendedAt = &now
	} else if !on {
		user.SuspendedAt = nil
	}

	out := &AccountSuspension{User: &user, VMsFailed: []string{}}
	if !withVMs {
		return out, nil
	}
	vms, err := s.vmRepo.ListByUserID(ctx, userID, 0, 0)
	if err != nil {
		return out, fmt.Errorf("the account is updated, but its VMs could not be listed: %w", err)
	}
	// ponytail: one VM at a time, each a call to its node; queue it if accounts
	// grow past a few dozen VMs.
	for i := range vms {
		vm := &vms[i]
		if vm.Status == models.VMStatusDeleting || vm.Suspended == on {
			continue
		}
		if on {
			err = s.SuspendVM(ctx, vm.ID)
		} else {
			err = s.UnsuspendVM(ctx, vm.ID)
		}
		out.VMsChanged++
		if err != nil {
			s.logger.WarnContext(ctx, "account suspension: VM not fully changed", "vm_id", vm.ID, "suspend", on, "error", err)
			out.VMsFailed = append(out.VMsFailed, vm.Hostname)
		}
	}
	return out, nil
}

// AuditAccountSuspension records a suspend or restore of an account. actorID
// is empty when the billing system did it.
func AuditAccountSuspension(ctx context.Context, db *gorm.DB, on bool, actorID, userID, ip, userAgent string, details map[string]any) {
	action := "user.unsuspend"
	if on {
		action = "user.suspend"
	}
	entry := &models.AuditLog{
		UserID:       strPtrOrNil(actorID),
		Action:       action,
		ResourceType: "USER",
		ResourceID:   &userID,
		IPAddress:    ip,
		UserAgent:    userAgent,
		Details:      details,
	}
	if err := db.WithContext(ctx).Create(entry).Error; err != nil {
		slog.Default().Error("audit: could not record account suspension", "action", action, "account", userID, "error", err)
	}
}
