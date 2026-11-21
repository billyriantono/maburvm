package service

import (
	"context"
	"encoding/json"

	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Floating IPs are billable, so WHMCS has to learn when a customer's chargeable
// count changes. The panel already pushes signed events for bandwidth overage;
// this reuses the same endpoint, the same HMAC secret and the same header, so an
// operator configures one webhook under Settings → System → API and gets both.
//
// The event carries the resulting counts rather than a delta. A delta would
// require WHMCS to have seen every prior event in order; a snapshot is correct
// even if one is missed or retried, which for billing matters more than being
// terse.

// The floating_ip.usage event carries: reason (ordered | released | attached |
// detached), user_id, user_ref (the billing system's own reference for the
// customer, when the panel knows it), address, total, free (one attached
// address is included) and billable (what to charge for).

// resolveBillingWebhook returns the WHMCS endpoint and signing secret from the
// admin-managed API settings, falling back to the env secret when unset — the
// same resolution the overage webhook uses.
func resolveBillingWebhook(ctx context.Context, db *gorm.DB) (url, secret string) {
	secret, _ = PanelValue("billingWebhookSecret")
	if db == nil {
		return "", secret
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = 'api'").
		Scan(&raw).Error; err != nil || raw == "" {
		return "", secret
	}
	var cfg struct {
		WebhookURL string `json:"webhookUrl"`
		HMACSecret string `json:"hmacSecret"`
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return "", secret
	}
	if s, err := models.OpenSecret(cfg.HMACSecret); err == nil && s != "" {
		secret = s
	}
	return cfg.WebhookURL, secret
}

// notifyFloatingIPBilling reports a customer's new chargeable count to the
// billing system.
//
// Best-effort on purpose: a billing endpoint being down must never stop a
// customer attaching or releasing an address. The event is delivered by the
// job queue with retries, and the counts are also readable at any time from
// /api/v1/floating-ips/billing.
func (s *VMService) notifyFloatingIPBilling(ctx context.Context, userID, address, reason string) {
	if userID == "" {
		return
	}
	total, free, billable, err := s.BillableFloatingIPs(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "floating IP billing: count failed", "user_id", userID, "error", err)
		return
	}
	fields := map[string]any{
		"reason": reason, "user_id": userID, "total": total, "free": free, "billable": billable,
	}
	if address != "" {
		fields["address"] = address
	}
	if ref := BillingUserRef(ctx, s.db, userID); ref != "" {
		fields["user_ref"] = ref
	}
	EmitBillingEvent(ctx, s.db, s.riverClient, s.logger, BillingEventFloatingIP, userID, fields)
}
