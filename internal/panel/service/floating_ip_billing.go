package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"time"

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

// FloatingIPBillingEvent is the payload sent to WHMCS.
type FloatingIPBillingEvent struct {
	Event    string    `json:"event"`  // always "floating_ip.usage"
	Reason   string    `json:"reason"` // ordered | released | attached | detached
	UserID   string    `json:"user_id"`
	Address  string    `json:"address,omitempty"`
	Total    int64     `json:"total"`
	Free     int64     `json:"free"`     // one attached address is included
	Billable int64     `json:"billable"` // what to charge for
	SentAt   time.Time `json:"sent_at"`
}

// resolveBillingWebhook returns the WHMCS endpoint and signing secret from the
// admin-managed API settings, falling back to the env secret when unset — the
// same resolution the overage webhook uses.
func resolveBillingWebhook(ctx context.Context, db *gorm.DB) (url, secret string) {
	secret = os.Getenv("BILLING_WEBHOOK_SECRET")
	if db == nil {
		return "", secret
	}
	var raw string
	if err := db.WithContext(ctx).
		Raw("SELECT data::text FROM system_settings WHERE section = 'api'").
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
	if cfg.HMACSecret != "" {
		secret = cfg.HMACSecret
	}
	return cfg.WebhookURL, secret
}

// notifyFloatingIPBilling reports a customer's new chargeable count to WHMCS.
//
// Best-effort on purpose: a billing endpoint being down must never stop a
// customer attaching or releasing an address. The counts are also readable at
// any time from /api/v1/floating-ips/billing, so a missed event self-corrects on
// the next one or on a poll — the webhook is an optimisation, not the source of
// truth.
func (s *VMService) notifyFloatingIPBilling(ctx context.Context, userID, address, reason string) {
	if userID == "" {
		return
	}
	total, free, billable, err := s.BillableFloatingIPs(ctx, userID)
	if err != nil {
		s.logger.WarnContext(ctx, "floating IP billing: count failed", "user_id", userID, "error", err)
		return
	}
	url, secret := resolveBillingWebhook(ctx, s.db)
	if url == "" {
		s.logger.InfoContext(ctx, "floating IP usage changed (no billing webhook configured)",
			"user_id", userID, "reason", reason, "billable", billable)
		return
	}
	body, _ := json.Marshal(FloatingIPBillingEvent{
		Event: "floating_ip.usage", Reason: reason, UserID: userID, Address: address,
		Total: total, Free: free, Billable: billable, SentAt: time.Now().UTC(),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		s.logger.WarnContext(ctx, "floating IP billing: build request failed", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-Webhook-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		s.logger.WarnContext(ctx, "floating IP billing: post failed", "user_id", userID, "error", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		s.logger.WarnContext(ctx, "floating IP billing: non-2xx", "user_id", userID, "status", resp.StatusCode)
		return
	}
	s.logger.InfoContext(ctx, "floating IP usage reported to billing",
		"user_id", userID, "reason", reason, "billable", billable)
}
