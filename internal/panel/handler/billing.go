// Package handler provides HTTP handlers for billing webhook operations
package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ============================================================================
// Configuration and Constants
// ============================================================================

const (
	// WebhookSignatureHeader is the header containing HMAC signature
	WebhookSignatureHeader = "X-Webhook-Signature"
	// WebhookAPIKeyHeader is the header containing API key
	WebhookAPIKeyHeader = "X-API-Key"
	// IdempotencyKeyHeader is the header for idempotency key
	IdempotencyKeyHeader = "X-Idempotency-Key"
	// WebhookTimestampHeader repeats the request timestamp. It is not signed,
	// so the panel does not rely on it: the body's own timestamp is what counts.
	WebhookTimestampHeader = "X-Webhook-Timestamp"

	// MaxWebhookAge is how far a request's signed timestamp may be from now
	MaxWebhookAge = 5 * time.Minute
	// replayKeyPrefix marks the rows that remember a signature already used.
	// They share the idempotency table and are never a caller's own key.
	replayKeyPrefix = "replay:"
	// IdempotencyCacheTTL is how long to cache idempotency keys (24 hours)
	IdempotencyCacheTTL = 24 * time.Hour
)

// WebhookEventType represents the type of billing webhook event
type WebhookEventType string

const (
	// EventVMCreate creates a new VM
	EventVMCreate WebhookEventType = "vm.create"
	// EventVMSuspend suspends a VM
	EventVMSuspend WebhookEventType = "vm.suspend"
	// EventVMUnsuspend unsuspends a VM
	EventVMUnsuspend WebhookEventType = "vm.unsuspend"
	// EventVMDestroy destroys a VM
	EventVMDestroy WebhookEventType = "vm.destroy"
	// EventVMResize changes CPU, memory and disk (a package change)
	EventVMResize WebhookEventType = "vm.resize"
	// EventVMResetPassword sets the guest root password
	EventVMResetPassword WebhookEventType = "vm.reset_password"
	// EventVMUsage reports this period's traffic and the disk allowance
	EventVMUsage WebhookEventType = "vm.usage"
	// EventVMStart, EventVMStop and EventVMRestart are staff power actions
	EventVMStart   WebhookEventType = "vm.start"
	EventVMStop    WebhookEventType = "vm.stop"
	EventVMRestart WebhookEventType = "vm.restart"
	// EventUserSuspend and EventUserUnsuspend lock a customer account out of
	// the panel and let it back in, optionally with its VMs.
	EventUserSuspend   WebhookEventType = "user.suspend"
	EventUserUnsuspend WebhookEventType = "user.unsuspend"
	// EventUserEnsure finds the customer account for a billing client by its
	// reference or email, or creates one, and returns its ID.
	EventUserEnsure WebhookEventType = "user.ensure"
	// EventVMStatus reports whether a VM is ready and how it is reached.
	EventVMStatus WebhookEventType = "vm.status"
	// EventCatalogList lists the templates, plans and regions an order can name.
	EventCatalogList WebhookEventType = "catalog.list"
	// EventUserSSO returns a one-time sign-in link for a customer account.
	EventUserSSO WebhookEventType = "user.sso"
)

// aboutUser reports an event that names an account, not a VM.
func (e WebhookEventType) aboutUser() bool {
	return e == EventUserSuspend || e == EventUserUnsuspend || e == EventUserEnsure || e == EventUserSSO
}

// aboutVM reports an event that acts on, or reads, one existing VM.
func (e WebhookEventType) aboutVM() bool {
	return e != EventVMCreate && e != EventCatalogList && !e.aboutUser()
}

// IsValid checks if the event type is valid
func (e WebhookEventType) IsValid() bool {
	switch e {
	case EventVMCreate, EventVMSuspend, EventVMUnsuspend, EventVMDestroy,
		EventVMResize, EventVMResetPassword, EventVMUsage,
		EventVMStart, EventVMStop, EventVMRestart,
		EventUserSuspend, EventUserUnsuspend, EventUserEnsure,
		EventVMStatus, EventCatalogList, EventUserSSO:
		return true
	}
	return false
}

// ============================================================================
// Webhook Request/Response Types
// ============================================================================

// WebhookPayload represents the incoming webhook payload structure
// Document for integrators:
//
//	{
//	  "event": "vm.create|vm.suspend|vm.unsuspend|vm.destroy|vm.resize|vm.reset_password|vm.usage|vm.status|vm.start|vm.stop|vm.restart|user.ensure|user.suspend|user.unsuspend|user.sso|catalog.list",
//	  "timestamp": "2024-01-01T00:00:00Z",
//	  "nonce": "a value that is new for every request",
//	  "data": {
//	    "user_id": "panel user id",
//	    "vm_specs": {"cpu": 2, "memory": 4096, "disk": 50},
//	    "template_id": "os template id",
//	    "hostname": "client-vm-1"
//	  }
//	}
type WebhookPayload struct {
	Event WebhookEventType `json:"event" validate:"required"`
	// Timestamp is signed with the body and must be within MaxWebhookAge of now.
	Timestamp time.Time `json:"timestamp" validate:"required"`
	// Nonce makes two otherwise identical requests in the same second distinct.
	// The panel does not read it: a signature is accepted once, and the nonce is
	// what gives a deliberate repeat a signature of its own.
	Nonce string      `json:"nonce,omitempty"`
	Data  WebhookData `json:"data" validate:"required"`
}

// WebhookData contains the event-specific data
type WebhookData struct {
	// UserID is the external user identifier
	UserID string `json:"user_id" validate:"required"`
	// VMSpecs contains VM resource specifications
	VMSpecs VMSpecs `json:"vm_specs,omitempty"`
	// TemplateID is the OS template identifier
	TemplateID string `json:"template_id,omitempty"`
	// Hostname for the VM
	Hostname string `json:"hostname,omitempty"`
	// VMID is the VM identifier (for suspend/unsuspend/destroy operations)
	VMID string `json:"vm_id,omitempty"`
	// ExternalRef is the billing system's reference for the service. Kept on
	// the VM at vm.create; later events that send one must send the same.
	ExternalRef string `json:"external_ref,omitempty"`
	// Password is the root password to set (vm.create, vm.reset_password)
	Password string `json:"password,omitempty"`
	// PlanID names a panel plan (vm.create, vm.resize). The plan then decides
	// the size, the speed cap and the transfer quota, and vm_specs is not needed.
	PlanID string `json:"plan_id,omitempty"`
	// WithVMs also suspends or restores the account's VMs (user.suspend, user.unsuspend)
	WithVMs bool `json:"with_vms,omitempty"`
	// Email and Name describe the billing client (user.ensure)
	Email string `json:"email,omitempty"`
	Name  string `json:"name,omitempty"`
	// Region is where to place the VM, by slug or ID (vm.create). Without one
	// the panel's default region is used, when one is set.
	Region string `json:"region,omitempty"`
	// BandwidthMbps and DataQuotaGB override the plan's speed cap and monthly
	// transfer quota (vm.create; vm.resize together with plan_id). 0 keeps the plan's.
	BandwidthMbps int   `json:"bandwidth_mbps,omitempty"`
	DataQuotaGB   int64 `json:"data_quota_gb,omitempty"`
}

// VMSpecs represents VM resource specifications
type VMSpecs struct {
	CPU    int `json:"cpu" validate:"required,min=1,max=128"`
	Memory int `json:"memory" validate:"required,min=128,max=131072"` // MB
	Disk   int `json:"disk" validate:"required,min=1,max=1048576"`    // GB
}

// WebhookResponse represents the response to a webhook event
type WebhookResponse struct {
	Success     bool              `json:"success"`
	Message     string            `json:"message"`
	VMID        string            `json:"vm_id,omitempty"`
	ExternalRef string            `json:"external_ref,omitempty"`
	Timestamp   time.Time         `json:"timestamp"`
	RequestID   string            `json:"request_id"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	// Data carries a structured answer (catalog.list)
	Data interface{} `json:"data,omitempty"`
}

// IdempotencyRecord stores the result of a processed webhook
type IdempotencyRecord struct {
	RequestID   string
	Event       string
	Response    WebhookResponse
	ProcessedAt time.Time
}

// ============================================================================
// BillingHandler
// ============================================================================

// BillingHandler handles billing webhook operations
type BillingHandler struct {
	vmService *service.VMService
	// networkService applies a plan's speed cap and quota on a package change.
	// Optional: without it a package change alters the size only.
	networkService *service.NetworkService
	logger         *slog.Logger
	webhookSecret  string
	apiKey         string

	// db backs the persistent idempotency store (survives restarts). When nil
	// (e.g. in tests) idempotency is simply not deduplicated.
	db *gorm.DB
}

// billingIdempotencyRow is the persisted result of a processed webhook, keyed by
// the caller-supplied idempotency key, so replays return the original response
// even across panel restarts.
type billingIdempotencyRow struct {
	IdempotencyKey string    `gorm:"column:idempotency_key;primaryKey"`
	RequestID      string    `gorm:"column:request_id"`
	Event          string    `gorm:"column:event"`
	Response       []byte    `gorm:"column:response;type:jsonb"`
	ProcessedAt    time.Time `gorm:"column:processed_at"`
	ExpiresAt      time.Time `gorm:"column:expires_at"`
}

func (billingIdempotencyRow) TableName() string { return "billing_idempotency" }

// NewBillingHandler creates a new BillingHandler instance. db backs the
// persistent idempotency store; pass nil to disable deduplication.
func NewBillingHandler(vmService *service.VMService, logger *slog.Logger, db *gorm.DB) *BillingHandler {
	h := &BillingHandler{
		vmService: vmService,
		logger:    logger,
		db:        db,
	}
	if db != nil {
		// Periodically evict expired idempotency rows for the life of the process.
		go func() {
			ticker := time.NewTicker(1 * time.Hour)
			defer ticker.Stop()
			for range ticker.C {
				h.cleanupOldIdempotency(context.Background())
			}
		}()
	}
	return h
}

// SetNetworkService wires what a package change needs to apply a plan's speed
// cap and transfer quota.
func (h *BillingHandler) SetNetworkService(n *service.NetworkService) { h.networkService = n }

// credentials are the webhook's API key and signing secret: the handler's own
// when set (tests), else System settings, Panel, else BILLING_API_KEY and
// BILLING_WEBHOOK_SECRET. Read per request so a change applies at once. Empty
// when unset: the webhook then fails closed (see RequireWebhookAuth) rather
// than accepting a known default that would let anyone forge billing events.
func (h *BillingHandler) credentials() (apiKey, secret string) {
	apiKey, secret = h.apiKey, h.webhookSecret
	if apiKey == "" {
		apiKey, _ = service.PanelValue("billingApiKey")
	}
	if secret == "" {
		secret, _ = service.PanelValue("billingWebhookSecret")
	}
	return apiKey, secret
}

// ============================================================================
// Authentication & Security Middleware
// ============================================================================

// RequireWebhookAuth middleware validates API key and HMAC signature
func (h *BillingHandler) RequireWebhookAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			// 0. Fail closed if the billing credentials aren't configured. Without
			// this, an unset BILLING_API_KEY/BILLING_WEBHOOK_SECRET would otherwise
			// accept a publicly-known default, letting anyone provision/destroy VMs.
			wantKey, secret := h.credentials()
			if wantKey == "" || secret == "" {
				h.logger.Error("billing webhook is disabled: set the billing API key and signing secret under System settings, Panel (or BILLING_API_KEY and BILLING_WEBHOOK_SECRET)")
				return c.JSON(http.StatusServiceUnavailable, WebhookResponse{
					Success:   false,
					Message:   "billing webhook is not configured",
					Timestamp: time.Now().UTC(),
				})
			}

			// 1. Validate API Key
			apiKey := c.Request().Header.Get(WebhookAPIKeyHeader)
			if apiKey == "" {
				return c.JSON(http.StatusUnauthorized, WebhookResponse{
					Success:   false,
					Message:   "API key is required",
					Timestamp: time.Now().UTC(),
				})
			}

			if subtle.ConstantTimeCompare([]byte(apiKey), []byte(wantKey)) != 1 {
				h.logger.Warn("Invalid API key attempt",
					"ip", c.RealIP(),
					"path", c.Path(),
				)
				return c.JSON(http.StatusUnauthorized, WebhookResponse{
					Success:   false,
					Message:   "Invalid API key",
					Timestamp: time.Now().UTC(),
				})
			}

			// 2. Validate HMAC signature
			signature := c.Request().Header.Get(WebhookSignatureHeader)
			if signature == "" {
				return c.JSON(http.StatusUnauthorized, WebhookResponse{
					Success:   false,
					Message:   "Webhook signature is required",
					Timestamp: time.Now().UTC(),
				})
			}

			// Read body for signature verification
			body, err := io.ReadAll(c.Request().Body)
			if err != nil {
				return c.JSON(http.StatusBadRequest, WebhookResponse{
					Success:   false,
					Message:   "Failed to read request body",
					Timestamp: time.Now().UTC(),
				})
			}
			// Restore body for later use
			c.Request().Body = io.NopCloser(bytes.NewReader(body))

			if !h.verifySignature(body, signature) {
				h.logger.Warn("Invalid webhook signature",
					"ip", c.RealIP(),
					"path", c.Path(),
				)
				return c.JSON(http.StatusUnauthorized, WebhookResponse{
					Success:   false,
					Message:   "Invalid webhook signature",
					Timestamp: time.Now().UTC(),
				})
			}

			// 3. Replay protection, on what was signed. The X-Webhook-Timestamp
			// header is not signed and is not trusted: the body's timestamp is.
			if err := checkSignedTimestamp(body, time.Now()); err != nil {
				return c.JSON(http.StatusBadRequest, WebhookResponse{
					Success:   false,
					Message:   err.Error(),
					Timestamp: time.Now().UTC(),
				})
			}
			fresh, err := h.firstUse(c.Request().Context(), h.sign(body))
			if err != nil {
				h.logger.Error("billing webhook: replay check failed", "error", err)
				return c.JSON(http.StatusServiceUnavailable, WebhookResponse{
					Success:   false,
					Message:   "The panel could not check this request against earlier ones. Try again.",
					Timestamp: time.Now().UTC(),
				})
			}
			if !fresh {
				h.logger.Warn("billing webhook: replayed request refused", "ip", c.RealIP())
				return c.JSON(http.StatusConflict, WebhookResponse{
					Success:   false,
					Message:   "Duplicate request: this signed request was already received. Send a new one with a current timestamp and a new nonce.",
					Timestamp: time.Now().UTC(),
				})
			}

			return next(c)
		}
	}
}

// sign returns the hex HMAC-SHA256 of the payload under the webhook secret.
func (h *BillingHandler) sign(payload []byte) string {
	_, secret := h.credentials()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifySignature verifies the HMAC-SHA256 signature of the webhook payload
func (h *BillingHandler) verifySignature(payload []byte, signature string) bool {
	// Remove "sha256=" prefix if present
	sig := strings.TrimPrefix(signature, "sha256=")
	// Constant-time comparison to prevent timing attacks
	return hmac.Equal([]byte(sig), []byte(h.sign(payload)))
}

// checkSignedTimestamp requires the body's own timestamp, and requires it to
// be within MaxWebhookAge of now. Without it a captured request stays valid
// for ever.
func checkSignedTimestamp(body []byte, now time.Time) error {
	var signed struct {
		Timestamp time.Time `json:"timestamp"`
	}
	if err := json.Unmarshal(body, &signed); err != nil || signed.Timestamp.IsZero() {
		return errors.New("timestamp is required in the request body, in RFC 3339 form")
	}
	age := now.Sub(signed.Timestamp)
	if age < 0 {
		age = -age
	}
	if age > MaxWebhookAge {
		return fmt.Errorf("request timestamp is more than %d minutes from the panel's clock; check the clock of the billing server", int(MaxWebhookAge.Minutes()))
	}
	return nil
}

// firstUse records a verified signature and reports whether this is the first
// request to carry it. A signature covers the timestamp, so it is remembered
// for as long as that timestamp could still be accepted; after that the
// timestamp check refuses the request by itself.
func (h *BillingHandler) firstUse(ctx context.Context, signature string) (bool, error) {
	if h.db == nil {
		return true, nil
	}
	now := time.Now().UTC()
	res := h.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&billingIdempotencyRow{
		IdempotencyKey: replayKeyPrefix + signature,
		RequestID:      uuid.New().String(),
		Event:          "replay-guard",
		Response:       []byte("{}"),
		ProcessedAt:    now,
		ExpiresAt:      now.Add(2 * MaxWebhookAge),
	})
	return res.RowsAffected == 1, res.Error
}

// dropIdempotency forgets a stored result.
func (h *BillingHandler) dropIdempotency(ctx context.Context, idempotencyKey string) {
	if h.db == nil {
		return
	}
	if err := h.db.WithContext(ctx).Where("idempotency_key = ?", idempotencyKey).
		Delete(&billingIdempotencyRow{}).Error; err != nil {
		h.logger.Error("failed to drop idempotency record", "error", err, "idempotency_key", idempotencyKey)
	}
}

// createdVMGone reports a stored vm.create result whose VM has since been
// destroyed. Replaying it would answer a new order with a VM that is gone.
func (h *BillingHandler) createdVMGone(ctx context.Context, record *IdempotencyRecord) bool {
	if record.Event != string(EventVMCreate) || record.Response.VMID == "" || h.vmService == nil {
		return false
	}
	// The row, not GetVMStatus: that reports a suspended VM as suspended even
	// while it is being deleted, which is how an unpaid service is terminated.
	vm, err := h.vmService.GetVMRecord(ctx, record.Response.VMID)
	return errors.Is(err, service.ErrVMNotFound) || (err == nil && vm.Status == models.VMStatusDeleting)
}

// ============================================================================
// Idempotency Support
// ============================================================================

// checkIdempotency checks if a request has already been processed
func (h *BillingHandler) checkIdempotency(ctx context.Context, idempotencyKey string) (*IdempotencyRecord, bool) {
	if h.db == nil {
		return nil, false
	}

	var row billingIdempotencyRow
	err := h.db.WithContext(ctx).
		Where("idempotency_key = ? AND expires_at > ?", idempotencyKey, time.Now().UTC()).
		First(&row).Error
	if err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Error("failed to read idempotency record", "error", err, "idempotency_key", idempotencyKey)
		}
		return nil, false
	}

	var response WebhookResponse
	if err := json.Unmarshal(row.Response, &response); err != nil {
		h.logger.Error("failed to decode stored idempotency response", "error", err, "idempotency_key", idempotencyKey)
		return nil, false
	}

	return &IdempotencyRecord{
		RequestID:   row.RequestID,
		Event:       row.Event,
		Response:    response,
		ProcessedAt: row.ProcessedAt,
	}, true
}

// storeIdempotency persists the result of a processed request. It uses an
// insert-on-conflict-do-nothing so a concurrent duplicate (two identical
// webhooks racing) keeps the first result rather than erroring.
func (h *BillingHandler) storeIdempotency(ctx context.Context, idempotencyKey, event string, record *IdempotencyRecord) {
	if h.db == nil {
		return
	}

	payload, err := json.Marshal(record.Response)
	if err != nil {
		h.logger.Error("failed to encode idempotency response", "error", err, "idempotency_key", idempotencyKey)
		return
	}

	row := billingIdempotencyRow{
		IdempotencyKey: idempotencyKey,
		RequestID:      record.RequestID,
		Event:          event,
		Response:       payload,
		ProcessedAt:    record.ProcessedAt,
		ExpiresAt:      record.ProcessedAt.Add(IdempotencyCacheTTL),
	}

	if err := h.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&row).Error; err != nil {
		h.logger.Error("failed to store idempotency record", "error", err, "idempotency_key", idempotencyKey)
	}
}

// cleanupOldIdempotency removes expired idempotency records. Safe to call
// periodically; a no-op when the store is disabled.
func (h *BillingHandler) cleanupOldIdempotency(ctx context.Context) {
	if h.db == nil {
		return
	}
	if err := h.db.WithContext(ctx).
		Where("expires_at < ?", time.Now().UTC()).
		Delete(&billingIdempotencyRow{}).Error; err != nil {
		h.logger.Error("failed to clean up expired idempotency records", "error", err)
	}
}

// ============================================================================
// Main Webhook Handler
// ============================================================================

// HandleWebhook handles POST /webhooks/billing - Receive billing system events
func (h *BillingHandler) HandleWebhook(c echo.Context) error {
	requestID := uuid.New().String()
	now := time.Now().UTC()

	// Check idempotency key
	idempotencyKey := c.Request().Header.Get(IdempotencyKeyHeader)
	if strings.HasPrefix(idempotencyKey, replayKeyPrefix) {
		return c.JSON(http.StatusBadRequest, WebhookResponse{
			Success: false, Message: "Invalid idempotency key", Timestamp: now, RequestID: requestID,
		})
	}
	if idempotencyKey != "" {
		if record, exists := h.checkIdempotency(c.Request().Context(), idempotencyKey); exists {
			if h.createdVMGone(c.Request().Context(), record) {
				// The service is being provisioned again after its VM was
				// destroyed: this is a new order, not a retry of the old one.
				h.dropIdempotency(c.Request().Context(), idempotencyKey)
			} else {
				h.logger.Info("Returning cached idempotency response",
					"request_id", requestID,
					"idempotency_key", idempotencyKey,
				)
				// Return cached response with new request ID
				response := record.Response
				response.RequestID = requestID
				response.Timestamp = now
				return c.JSON(http.StatusOK, response)
			}
		}
	}

	// Parse webhook payload
	var payload WebhookPayload
	if err := c.Bind(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, WebhookResponse{
			Success:   false,
			Message:   "Invalid request body: " + err.Error(),
			Timestamp: now,
			RequestID: requestID,
		})
	}

	// Validate event type
	if !payload.Event.IsValid() {
		return c.JSON(http.StatusBadRequest, WebhookResponse{
			Success:   false,
			Message:   fmt.Sprintf("Invalid event type: %s", payload.Event),
			Timestamp: now,
			RequestID: requestID,
		})
	}

	// Validate required fields based on event type
	if err := h.validatePayload(&payload); err != nil {
		return c.JSON(http.StatusBadRequest, WebhookResponse{
			Success:   false,
			Message:   err.Error(),
			Timestamp: now,
			RequestID: requestID,
		})
	}

	// Process the event
	var response WebhookResponse
	var err error

	ctx := c.Request().Context()

	// A sign-in link to a VM that is gone opens the customer's dashboard.
	if payload.Event == EventUserSSO && payload.Data.VMID != "" {
		if _, err := h.vmService.GetVMRecord(ctx, payload.Data.VMID); errors.Is(err, service.ErrVMNotFound) {
			payload.Data.VMID = ""
		}
	}
	// An event about an existing VM must come from the service that owns it.
	if payload.Event.aboutVM() || (payload.Event == EventUserSSO && payload.Data.VMID != "") {
		if err := h.checkEventOwner(ctx, payload.Event, &payload.Data); err != nil {
			status := http.StatusBadRequest
			switch {
			case errors.Is(err, service.ErrVMNotFound):
				status = http.StatusNotFound
			case errors.Is(err, errBillingOwnerMismatch), errors.Is(err, errBillingServiceMismatch):
				status = http.StatusForbidden
				h.auditOwnerMismatch(c, &payload, requestID, err)
			}
			h.logger.Warn("Billing event refused", "event", payload.Event, "vm_id", payload.Data.VMID,
				"user_id", payload.Data.UserID, "request_id", requestID, "error", err)
			return c.JSON(status, WebhookResponse{Success: false, Message: err.Error(), Timestamp: now, RequestID: requestID})
		}
	}

	// Polls that only read would flood the audit log with a row each.
	if payload.Event == EventVMStatus || payload.Event == EventVMUsage || payload.Event == EventCatalogList {
		middleware.SkipAudit(c)
	}
	switch payload.Event {
	case EventVMCreate:
		response, err = h.handleVMCreate(ctx, &payload.Data, requestID)
	case EventVMSuspend:
		response, err = h.handleVMSuspend(ctx, &payload.Data, requestID)
	case EventVMUnsuspend:
		response, err = h.handleVMUnsuspend(ctx, &payload.Data, requestID)
	case EventVMDestroy:
		response, err = h.handleVMDestroy(ctx, &payload.Data, requestID)
	case EventVMResize:
		response, err = h.handleVMResize(ctx, &payload.Data, requestID)
	case EventVMResetPassword:
		response, err = h.handleVMResetPassword(ctx, &payload.Data, requestID)
	case EventVMUsage:
		response, err = h.handleVMUsage(ctx, &payload.Data, requestID)
	case EventVMStart:
		response, err = h.handleVMPower(ctx, &payload.Data, requestID, service.LifecycleStart)
	case EventVMStop:
		response, err = h.handleVMPower(ctx, &payload.Data, requestID, service.LifecycleStop)
	case EventVMRestart:
		response, err = h.handleVMPower(ctx, &payload.Data, requestID, service.LifecycleRestart)
	case EventUserSuspend:
		response, err = h.handleUserSuspend(c, &payload.Data, requestID, true)
	case EventUserUnsuspend:
		response, err = h.handleUserSuspend(c, &payload.Data, requestID, false)
	case EventUserEnsure:
		response, err = h.handleUserEnsure(c, &payload.Data, requestID)
	case EventVMStatus:
		response, err = h.handleVMStatus(ctx, &payload.Data, requestID)
	case EventCatalogList:
		response, err = h.handleCatalogList(ctx, requestID)
	case EventUserSSO:
		response, err = h.handleUserSSO(ctx, &payload.Data, requestID)
	}

	if err != nil {
		h.logger.Error("Webhook event processing failed",
			"event", payload.Event,
			"request_id", requestID,
			"error", err,
		)

		statusCode := http.StatusInternalServerError
		switch {
		case errors.Is(err, service.ErrVMNotFound), errors.Is(err, service.ErrPlanNotFound), errors.Is(err, service.ErrUserNotFound),
			errors.Is(err, service.ErrRegionNotFound), errors.Is(err, service.ErrTemplateNotFound):
			statusCode = http.StatusNotFound
		case errors.Is(err, service.ErrPlanInactive), errors.Is(err, service.ErrNotACustomerAccount),
			errors.Is(err, service.ErrBillingUserInvalid), errors.Is(err, service.ErrTemplateNotOffered):
			statusCode = http.StatusBadRequest
		case errors.Is(err, service.ErrBillingStaffAccount), errors.Is(err, service.ErrRegionNoCapacity):
			statusCode = http.StatusConflict
		case errors.Is(err, errBillingSSORefused):
			statusCode = http.StatusForbidden
		}

		response = WebhookResponse{
			Success:   false,
			Message:   err.Error(),
			Timestamp: now,
			RequestID: requestID,
		}
		return c.JSON(statusCode, response)
	}

	// Only create and destroy are cached, the two that must not run twice.
	// Everything else is safe to repeat, and the billing module reuses one key
	// per service and action: a cached "suspended" would answer the next
	// suspend after an unsuspend without doing anything.
	if idempotencyKey != "" && (payload.Event == EventVMCreate || payload.Event == EventVMDestroy) {
		h.storeIdempotency(ctx, idempotencyKey, string(payload.Event), &IdempotencyRecord{
			RequestID:   requestID,
			Response:    response,
			ProcessedAt: now,
		})
	}

	return c.JSON(http.StatusOK, response)
}

// errBillingOwnerMismatch marks an event whose VM belongs to someone else.
var errBillingOwnerMismatch = errors.New("the VM does not belong to the user this event names")

// errBillingServiceMismatch marks an event from a service other than the one
// the VM was provisioned for.
var errBillingServiceMismatch = errors.New("the VM belongs to a different billing service than the one this event names")

// checkEventOwner refuses an event whose vm_id is not a VM of the user_id sent
// with it. The billing system keeps the VM id in a field staff can edit, and a
// wrong, copied or stale id would otherwise suspend, resize or destroy another
// customer's VM.
func (h *BillingHandler) checkEventOwner(ctx context.Context, event WebhookEventType, data *WebhookData) error {
	claimed := strings.TrimSpace(data.UserID)
	if claimed == "" {
		// The usage sync and a status read name no user and change nothing.
		if event == EventVMUsage || event == EventVMStatus {
			return nil
		}
		return fmt.Errorf("user_id is required for %s: set the panel user ID on this service", event)
	}
	vm, err := h.vmService.GetVMRecord(ctx, data.VMID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(vm.UserID, claimed) {
		return fmt.Errorf("%w: check the VM ID and the panel user ID stored on this service", errBillingOwnerMismatch)
	}
	// Two services of one customer: the owner matches, the service does not.
	// VMs with no reference on record, and events that send none, pass.
	if ref := strings.TrimSpace(data.ExternalRef); ref != "" && vm.BillingManaged() && *vm.BillingRef != ref {
		return fmt.Errorf("%w: check the VM ID stored on this service", errBillingServiceMismatch)
	}
	return nil
}

// auditOwnerMismatch records a refused event against the VM it aimed at.
func (h *BillingHandler) auditOwnerMismatch(c echo.Context, payload *WebhookPayload, requestID string, cause error) {
	if h.db == nil {
		return
	}
	vmID := payload.Data.VMID
	action := "billing.owner_mismatch"
	if errors.Is(cause, errBillingServiceMismatch) {
		action = "billing.service_mismatch"
	}
	entry := &models.AuditLog{
		Action:       action,
		ResourceType: "VM",
		ResourceID:   &vmID,
		IPAddress:    c.RealIP(),
		UserAgent:    c.Request().UserAgent(),
		Details: map[string]any{
			"event":           string(payload.Event),
			"claimed_user_id": payload.Data.UserID,
			"external_ref":    payload.Data.ExternalRef,
			"request_id":      requestID,
			"refused":         true,
		},
	}
	if err := h.db.WithContext(c.Request().Context()).Create(entry).Error; err != nil {
		h.logger.Error("audit: could not record a refused billing event", "vm_id", vmID, "error", err)
	}
}

// validatePayload validates the payload based on event type
func (h *BillingHandler) validatePayload(payload *WebhookPayload) error {
	// Kept on the VM in a 255-character column: say so here, not as a database error.
	if len(payload.Data.ExternalRef) > 255 {
		return errors.New("external_ref is longer than 255 characters")
	}
	if d := payload.Data; d.BandwidthMbps < 0 || d.BandwidthMbps > 100000 || d.DataQuotaGB < 0 {
		return errors.New("bandwidth_mbps must be between 0 and 100000, and data_quota_gb 0 or more")
	}
	switch payload.Event {
	case EventCatalogList:
		return nil
	case EventUserEnsure:
		if strings.TrimSpace(payload.Data.Email) == "" {
			return errors.New("email is required for user.ensure")
		}
		return nil
	case EventVMCreate:
		if payload.Data.UserID == "" {
			return errors.New("user_id is required for vm.create")
		}
		if payload.Data.Hostname == "" {
			return errors.New("hostname is required for vm.create")
		}
		if payload.Data.TemplateID == "" {
			return errors.New("template_id is required for vm.create")
		}
		if payload.Data.PlanID != "" {
			return nil // the plan decides the size
		}
		if payload.Data.VMSpecs.CPU == 0 {
			return errors.New("vm_specs.cpu is required for vm.create")
		}
		if payload.Data.VMSpecs.Memory == 0 {
			return errors.New("vm_specs.memory is required for vm.create")
		}
		if payload.Data.VMSpecs.Disk == 0 {
			return errors.New("vm_specs.disk is required for vm.create")
		}

	case EventUserSuspend, EventUserUnsuspend:
		if payload.Data.UserID == "" {
			return fmt.Errorf("user_id is required for %s", payload.Event)
		}

	case EventUserSSO:
		if payload.Data.UserID == "" {
			return errors.New("user_id is required for user.sso")
		}
		if payload.Data.VMID != "" {
			if _, err := uuid.Parse(payload.Data.VMID); err != nil {
				return errors.New("vm_id must be a VM ID")
			}
		}

	default:
		if payload.Data.VMID == "" {
			return fmt.Errorf("vm_id is required for %s", payload.Event)
		}
		if payload.Event == EventVMResetPassword && len(payload.Data.Password) < 8 {
			return errors.New("password of at least 8 characters is required for vm.reset_password")
		}
		if sp := payload.Data.VMSpecs; payload.Event == EventVMResize && payload.Data.PlanID == "" && (sp.CPU < 1 || sp.Memory < 128 || sp.Disk < 1) {
			return errors.New("plan_id, or vm_specs.cpu, memory and disk, is required for vm.resize")
		}
	}

	return nil
}

// ============================================================================
// Event Handlers
// ============================================================================

// billedCreateRequest turns a vm.create event into an order. Like one placed
// in the portal it gets a public address from the node's pools and a root
// password: the service's own when it is long enough to set, otherwise a
// generated one, kept for the owner to read on the VM's page.
func billedCreateRequest(data *WebhookData) *service.CreateVMRequest {
	password := data.Password
	if len(password) < 8 {
		password = ""
	}
	return &service.CreateVMRequest{
		UserID:       data.UserID,
		Hostname:     data.Hostname,
		OSTemplateID: data.TemplateID,
		// Ignored when a plan is named: the plan decides the size.
		Resources:          models.Resources{CPU: data.VMSpecs.CPU, RAM: data.VMSpecs.Memory, Disk: data.VMSpecs.Disk},
		PlanID:             data.PlanID,
		Region:             strings.TrimSpace(data.Region),
		BandwidthMbps:      data.BandwidthMbps,
		DataQuotaGB:        data.DataQuotaGB,
		Billed:             true,
		BillingRef:         strings.TrimSpace(data.ExternalRef),
		AnyHostname:        true,
		AutoAssignIP:       true,
		Password:           password,
		RegeneratePassword: password == "",
	}
}

// handleVMCreate creates a new VM based on billing system request
func (h *BillingHandler) handleVMCreate(
	ctx context.Context,
	data *WebhookData,
	requestID string,
) (WebhookResponse, error) {
	h.logger.Info("Processing vm.create webhook",
		"user_id", data.UserID,
		"hostname", data.Hostname,
		"request_id", requestID,
	)

	createReq := billedCreateRequest(data)

	// Create the VM
	resp, err := h.vmService.CreateVM(ctx, createReq)
	if err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to create VM: %w", err)
	}

	h.logger.Info("VM created successfully via webhook",
		"vm_id", resp.VM.ID,
		"hostname", data.Hostname,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM created successfully",
		VMID:        resp.VM.ID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"job_id":   fmt.Sprintf("%d", resp.JobID),
			"status":   resp.Status,
			"node_id":  resp.VM.NodeID,
			"hostname": resp.VM.Hostname,
		},
	}, nil
}

// handleVMSuspend flags the VM suspended and powers it off.
func (h *BillingHandler) handleVMSuspend(
	ctx context.Context,
	data *WebhookData,
	requestID string,
) (WebhookResponse, error) {
	h.logger.Info("Processing vm.suspend webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	// First, check if VM exists
	vm, err := h.vmService.GetVM(ctx, data.VMID, false)
	if err != nil {
		return WebhookResponse{}, err
	}

	if err := h.vmService.SuspendVM(ctx, data.VMID); err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to suspend VM: %w", err)
	}

	h.logger.Info("VM suspended successfully via webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM suspended (powered off, owner cannot start it)",
		VMID:        data.VMID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"previous_status": string(vm.VM.Status),
			"action":          "suspended_and_powered_off",
		},
	}, nil
}

// handleVMUnsuspend resumes a suspended VM
func (h *BillingHandler) handleVMUnsuspend(
	ctx context.Context,
	data *WebhookData,
	requestID string,
) (WebhookResponse, error) {
	h.logger.Info("Processing vm.unsuspend webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	// Check if VM exists
	vm, err := h.vmService.GetVM(ctx, data.VMID, false)
	if err != nil {
		return WebhookResponse{}, err
	}

	if err = h.vmService.UnsuspendVM(ctx, data.VMID); err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to unsuspend VM: %w", err)
	}

	h.logger.Info("VM unsuspended successfully via webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM unsuspended and started",
		VMID:        data.VMID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"previous_status": string(vm.VM.Status),
			"action":          "unsuspended_and_started",
		},
	}, nil
}

// handleVMDestroy deletes a VM and cleans up resources
func (h *BillingHandler) handleVMDestroy(
	ctx context.Context,
	data *WebhookData,
	requestID string,
) (WebhookResponse, error) {
	h.logger.Info("Processing vm.destroy webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	// Delete the VM (this will enqueue a cleanup job)
	err := h.vmService.DeleteVM(ctx, data.VMID)
	if err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to destroy VM: %w", err)
	}

	h.logger.Info("VM destroyed successfully via webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM destroyed successfully (cleanup in progress)",
		VMID:        data.VMID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"action": "deleted_and_cleanup_initiated",
		},
	}, nil
}

// handleVMResize applies a package change. The disk only grows: a smaller
// disk in the new package leaves the current one as it is. A package that
// names a plan also takes the plan's speed cap and transfer quota.
func (h *BillingHandler) handleVMResize(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	current, err := h.vmService.GetVM(ctx, data.VMID, false)
	if err != nil {
		return WebhookResponse{}, err
	}
	cpu, ram, disk := data.VMSpecs.CPU, data.VMSpecs.Memory, data.VMSpecs.Disk
	var plan *models.Plan
	if data.PlanID != "" {
		// Active or not: a VM may be moved onto, or re-synced with, a plan that
		// is no longer offered to new orders.
		if plan, err = h.vmService.GetPlan(ctx, data.PlanID); err != nil {
			return WebhookResponse{}, err
		}
		cpu, ram, disk = plan.CPU, plan.RAM, plan.Disk
	}
	old := current.VM.Resources
	res := old
	res.CPU, res.RAM = cpu, ram
	if disk > res.Disk {
		res.Disk = disk
	}

	// The plan's CPU and disk caps, before any shutdown, so the running VM
	// takes them live and a restart for the resize keeps them.
	var limitsNote string
	if plan != nil {
		applied, err := h.vmService.ApplyPlanVMLimits(ctx, data.VMID, plan)
		if err != nil {
			return WebhookResponse{}, fmt.Errorf("failed to apply the plan's CPU and disk limits: %w", err)
		}
		if applied.Note != nil {
			limitsNote = *applied.Note
		}
	}

	// A change of speed or quota alone needs no shutdown.
	if res.CPU != old.CPU || res.RAM != old.RAM || res.Disk != old.Disk {
		// CPU and memory only change on a stopped VM. A running one is shut down
		// here and booted again by the resize job once the new size is applied.
		wasRunning := current.VM.Status == models.VMStatusRunning
		if wasRunning {
			if _, err := h.vmService.ExecuteLifecycleCommand(ctx, &service.LifecycleRequest{
				VMID: data.VMID, Command: service.LifecycleStop,
			}); err != nil {
				return WebhookResponse{}, fmt.Errorf("failed to stop VM for resize: %w", err)
			}
		}
		if _, err := h.vmService.UpdateVM(ctx, &service.UpdateVMRequest{VMID: data.VMID, Resources: &res, StartAfter: wasRunning, Billed: true}); err != nil {
			if wasRunning { // do not leave the customer's VM off over a refused resize
				_, _ = h.vmService.ExecuteLifecycleCommand(ctx, &service.LifecycleRequest{
					VMID: data.VMID, Command: service.LifecycleStart,
				})
			}
			return WebhookResponse{}, fmt.Errorf("failed to resize VM: %w", err)
		}
	}
	meta := map[string]string{
		"cpu":    fmt.Sprintf("%d", res.CPU),
		"memory": fmt.Sprintf("%d", res.RAM),
		"disk":   fmt.Sprintf("%d", res.Disk),
	}
	if plan != nil {
		if h.networkService != nil {
			// The order's own speed and quota, when it names them, over the plan's.
			limits := *plan
			if data.BandwidthMbps > 0 {
				limits.BandwidthMbps = data.BandwidthMbps
			}
			if data.DataQuotaGB > 0 {
				limits.DataQuotaGB = data.DataQuotaGB
			}
			if err := h.networkService.ApplyPlanLimits(ctx, data.VMID, &limits); err != nil {
				return WebhookResponse{}, fmt.Errorf("the new size is applied, but not the plan's speed cap and transfer quota: %w", err)
			}
			meta["bandwidth_mbps"] = fmt.Sprintf("%d", limits.BandwidthMbps)
			meta["data_quota_gb"] = fmt.Sprintf("%d", limits.DataQuotaGB)
		}
		if _, err := h.vmService.SetBillingLink(ctx, data.VMID, &plan.ID, nil); err != nil {
			return WebhookResponse{}, err
		}
		meta["plan_id"] = plan.ID
		meta["limits"] = "applied"
		if limitsNote != "" {
			meta["limits"] = limitsNote
		}
	}
	return WebhookResponse{
		Success: true, Message: "VM resized", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID, Metadata: meta,
	}, nil
}

// handleUserSuspend locks a customer account out of the panel, or lets it back
// in, and on request does the same to its VMs.
func (h *BillingHandler) handleUserSuspend(c echo.Context, data *WebhookData, requestID string, on bool) (WebhookResponse, error) {
	ctx := c.Request().Context()
	userID := strings.TrimSpace(data.UserID)
	result, err := h.vmService.SetAccountSuspended(ctx, userID, on, data.WithVMs)
	if result == nil {
		return WebhookResponse{}, err
	}
	// The account is changed even when a VM could not be: say so, on record.
	if h.db != nil {
		service.AuditAccountSuspension(ctx, h.db, on, "", userID, c.RealIP(), c.Request().UserAgent(), map[string]any{
			"source": "billing", "with_vms": data.WithVMs, "vms_changed": result.VMsChanged,
			"vms_failed": result.VMsFailed, "external_ref": data.ExternalRef, "request_id": requestID,
		})
	}
	if err != nil {
		return WebhookResponse{}, err
	}
	message := "Account restored"
	if on {
		message = "Account suspended (cannot sign in; sessions and API keys stopped)"
	}
	return WebhookResponse{
		Success: true, Message: message, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID,
		Metadata: map[string]string{
			"vms_changed": fmt.Sprintf("%d", result.VMsChanged),
			"vms_failed":  strings.Join(result.VMsFailed, ","),
		},
	}, nil
}

// handleVMResetPassword queues a root password change on a running VM.
func (h *BillingHandler) handleVMResetPassword(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	resp, err := h.vmService.ResetPassword(ctx, &service.VMResetPasswordRequest{VMID: data.VMID, Password: data.Password})
	if err != nil {
		return WebhookResponse{}, err
	}
	return WebhookResponse{
		Success: true, Message: "Password reset queued", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID,
		Metadata: map[string]string{"job_id": fmt.Sprintf("%d", resp.JobID)},
	}, nil
}

// handleVMPower runs a staff start, stop or restart. A suspended VM refuses
// start and restart, the same as it does for its owner.
func (h *BillingHandler) handleVMPower(ctx context.Context, data *WebhookData, requestID string, cmd service.LifecycleCommand) (WebhookResponse, error) {
	if _, err := h.vmService.ExecuteLifecycleCommand(ctx, &service.LifecycleRequest{VMID: data.VMID, Command: cmd}); err != nil {
		return WebhookResponse{}, err
	}
	return WebhookResponse{
		Success: true, Message: "VM " + string(cmd) + " requested", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID,
	}, nil
}

// handleVMUsage reports the current period's traffic and the disk allowance,
// in megabytes, which is the unit billing systems store.
// ponytail: disk used is not reported; the panel does not measure it per VM.
func (h *BillingHandler) handleVMUsage(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	vm, err := h.vmService.GetVM(ctx, data.VMID, false)
	if err != nil {
		return WebhookResponse{}, err
	}
	const mb = 1 << 20
	meta := map[string]string{
		"bandwidth_used_mb":  "0",
		"bandwidth_limit_mb": "0",
		"disk_limit_mb":      fmt.Sprintf("%d", vm.VM.Resources.Disk*1024),
	}
	if h.db != nil {
		var usage models.BandwidthUsage
		if err := h.db.WithContext(ctx).Where("vm_id = ?", data.VMID).
			Order("period_start DESC").First(&usage).Error; err == nil {
			meta["bandwidth_used_mb"] = fmt.Sprintf("%d", usage.TotalBytes/mb)
			meta["bandwidth_limit_mb"] = fmt.Sprintf("%d", usage.QuotaBytes/mb)
		}
	}
	return WebhookResponse{
		Success: true, Message: "usage", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID, Metadata: meta,
	}, nil
}

// handleUserEnsure finds or creates the customer account of a billing client.
// A new account is emailed a link to choose its password.
func (h *BillingHandler) handleUserEnsure(c echo.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	if h.db == nil {
		return WebhookResponse{}, errors.New("accounts are not available")
	}
	ctx := c.Request().Context()
	user, created, err := service.EnsureBillingUser(ctx, h.db, data.Email, data.Name, data.ExternalRef)
	if err != nil {
		return WebhookResponse{}, err
	}
	message := "Customer account found"
	if created {
		message = "Customer account created"
		userID := user.ID.String()
		entry := &models.AuditLog{
			Action: "user.create", ResourceType: "USER", ResourceID: &userID,
			IPAddress: c.RealIP(), UserAgent: c.Request().UserAgent(),
			Details: map[string]any{"source": "billing", "email": user.Email, "external_ref": data.ExternalRef, "request_id": requestID},
		}
		if err := h.db.WithContext(ctx).Create(entry).Error; err != nil {
			h.logger.Error("audit: could not record an account created by billing", "user_id", userID, "error", err)
		}
	}
	return WebhookResponse{
		Success: true, Message: message, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID,
		Metadata: map[string]string{"user_id": user.ID.String(), "email": user.Email, "created": fmt.Sprintf("%t", created)},
	}, nil
}

// Provisioning states vm.status answers with.
const (
	provisionPending = "provisioning"
	provisionReady   = "ready"
	provisionFailed  = "failed"
	// provisionBroken is a VM that was installed and has since been left in an
	// error state by some later operation.
	provisionBroken = "error"
	provisionGone   = "deleting"
)

// handleVMStatus says whether a VM has finished provisioning and how it is
// reached. The password is never in the answer: the order named it.
func (h *BillingHandler) handleVMStatus(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	vm, err := h.vmService.GetVMRecord(ctx, data.VMID)
	if err != nil {
		return WebhookResponse{}, err
	}
	meta := map[string]string{"status": string(vm.Status), "hostname": vm.Hostname, "state": provisionReady}
	switch vm.Status {
	case models.VMStatusCreating:
		meta["state"] = provisionPending
	case models.VMStatusDeleting:
		meta["state"] = provisionGone
	case models.VMStatusError:
		// Any failed operation leaves a VM in "error"; only its creation
		// failing is a failed order. The queue says which, and why.
		switch job, reason := service.VMCreateJob(ctx, h.db, vm.ID); job {
		case "", "completed":
			meta["state"] = provisionBroken
		case "discarded", "cancelled":
			meta["state"], meta["error"] = provisionFailed, reason
		default:
			meta["state"], meta["error"], meta["retrying"] = provisionFailed, reason, "true"
		}
	}
	if h.db != nil {
		access := service.VMAccess(ctx, h.db, vm)
		meta["ip"], meta["ips"], meta["username"] = access.IP, strings.Join(access.IPs, ","), access.Username
	}
	return WebhookResponse{
		Success: true, Message: "status", VMID: vm.ID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID, Metadata: meta,
	}, nil
}

// handleCatalogList lists what an order can name, so the billing module can
// offer a choice instead of asking staff to paste IDs.
func (h *BillingHandler) handleCatalogList(ctx context.Context, requestID string) (WebhookResponse, error) {
	if h.db == nil {
		return WebhookResponse{}, errors.New("the catalog is not available")
	}
	db := h.db.WithContext(ctx)
	var templates []models.OSTemplate
	if err := db.Where("is_active = ?", true).Order("name, version").Find(&templates).Error; err != nil {
		return WebhookResponse{}, err
	}
	var plans []models.Plan
	if err := db.Where("is_active = ?", true).Order("cpu, ram, disk, name").Find(&plans).Error; err != nil {
		return WebhookResponse{}, err
	}
	var regions []models.Region
	if err := db.Where("enabled = ?", true).Order("name").Find(&regions).Error; err != nil {
		return WebhookResponse{}, err
	}
	type item = map[string]interface{}
	out := map[string][]item{"templates": {}, "plans": {}, "regions": {}}
	for _, t := range templates {
		planIDs := t.PlanIDs
		if planIDs == nil {
			planIDs = []string{}
		}
		out["templates"] = append(out["templates"], item{"id": t.ID, "name": t.Name, "version": t.Version,
			"os_type": t.OSType, "licensed": t.Licensed, "plan_ids": planIDs})
	}
	for _, p := range plans {
		out["plans"] = append(out["plans"], item{"id": p.ID, "name": p.Name, "cpu": p.CPU, "memory": p.RAM, "disk": p.Disk,
			"bandwidth_mbps": p.BandwidthMbps, "data_quota_gb": p.DataQuotaGB})
	}
	for _, r := range regions {
		out["regions"] = append(out["regions"], item{"id": r.ID, "slug": r.Slug, "name": r.Name, "country": r.Country})
	}
	return WebhookResponse{
		Success: true, Message: "catalog", Timestamp: time.Now().UTC(), RequestID: requestID, Data: out,
	}, nil
}

// ============================================================================
// Documentation Endpoint
// ============================================================================

// WebhookDocumentation represents the webhook documentation
type WebhookDocumentation struct {
	Title          string                 `json:"title"`
	Version        string                 `json:"version"`
	Endpoint       string                 `json:"endpoint"`
	Method         string                 `json:"method"`
	Authentication AuthDocumentation      `json:"authentication"`
	Events         []EventDocumentation   `json:"events"`
	PayloadExample map[string]interface{} `json:"payload_example"`
}

// AuthDocumentation describes authentication requirements
type AuthDocumentation struct {
	APIKey        string `json:"api_key_header"`
	Signature     string `json:"signature_header"`
	Timestamp     string `json:"timestamp_header"`
	SignatureAlgo string `json:"signature_algorithm"`
	Replay        string `json:"replay_protection"`
}

// EventDocumentation describes an event type
type EventDocumentation struct {
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    []string `json:"required_fields"`
	Optional    []string `json:"optional_fields"`
}

// GetWebhookDocumentation returns documentation for integrators
func (h *BillingHandler) GetWebhookDocumentation(c echo.Context) error {
	doc := WebhookDocumentation{
		Title:    "MaburVM Billing Webhook API",
		Version:  "1.0.0",
		Endpoint: "/webhooks/billing",
		Method:   "POST",
		Authentication: AuthDocumentation{
			APIKey:        WebhookAPIKeyHeader,
			Signature:     WebhookSignatureHeader,
			Timestamp:     WebhookTimestampHeader,
			SignatureAlgo: "HMAC-SHA256",
			Replay:        "The body's timestamp is required and must be within 5 minutes of the panel's clock. Each signed request is accepted once: send a new nonce in the body with every request.",
		},
		Events: []EventDocumentation{
			{
				Type:        string(EventVMCreate),
				Description: "Create a VM with a public address and a root password. With plan_id the plan decides size, speed cap and transfer quota. external_ref is kept on the VM as its billing service. region places it; without one the panel's default region is used. A template restricted to plans is refused on any other plan.",
				Required:    []string{"user_id", "hostname", "template_id", "plan_id or vm_specs.cpu, vm_specs.memory, vm_specs.disk"},
				Optional:    []string{"external_ref", "password", "region", "bandwidth_mbps", "data_quota_gb"},
			},
			{
				Type:        string(EventVMSuspend),
				Description: "Suspend a VM (power off; the owner cannot start it)",
				Required:    []string{"vm_id", "user_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMUnsuspend),
				Description: "Unsuspend a VM and start it",
				Required:    []string{"vm_id", "user_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMDestroy),
				Description: "Destroy a VM and cleanup resources. Like every event about an existing VM, refused when the VM does not belong to user_id",
				Required:    []string{"vm_id", "user_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMResize),
				Description: "Change CPU, memory and disk; a running VM is shut down and booted again, and disk only grows. With plan_id the plan's speed cap and transfer quota are applied as well.",
				Required:    []string{"vm_id", "user_id", "plan_id or vm_specs.cpu, vm_specs.memory, vm_specs.disk"},
				Optional:    []string{"bandwidth_mbps and data_quota_gb (with plan_id)"},
			},
			{
				Type:        string(EventVMResetPassword),
				Description: "Set the root password of a running VM",
				Required:    []string{"vm_id", "user_id", "password"},
			},
			{
				Type:        string(EventVMUsage),
				Description: "Traffic used and allowed this period and the disk allowance, in MB, returned in metadata",
				Required:    []string{"vm_id"},
			},
			{
				Type:        "vm.start | vm.stop | vm.restart",
				Description: "Power actions",
				Required:    []string{"vm_id", "user_id"},
			},
			{
				Type:        "user.suspend | user.unsuspend",
				Description: "Lock a customer account out of the panel (no sign-in; sessions and API keys stop) or let it back in. with_vms does the same to the account's VMs.",
				Required:    []string{"user_id"},
				Optional:    []string{"with_vms", "external_ref"},
			},
			{
				Type:        string(EventUserEnsure),
				Description: "Find the customer account for a billing client, by external_ref first and then by email, or create it. metadata.user_id is the account; metadata.created says it is new. A new account is emailed a link to choose its password. A staff account is refused.",
				Required:    []string{"email"},
				Optional:    []string{"name", "external_ref (the billing system's own reference for the client)"},
			},
			{
				Type:        string(EventVMStatus),
				Description: "Whether a VM has finished provisioning: metadata.state is provisioning, ready, failed (its installation failed: metadata.error says why, and metadata.retrying is true while the panel is still trying), error (installed, then left in an error state by a later operation) or deleting, with ip, ips (comma separated), username and hostname. The password is not returned.",
				Required:    []string{"vm_id"},
				Optional:    []string{"user_id", "external_ref"},
			},
			{
				Type:        string(EventCatalogList),
				Description: "The active templates (with licensed and plan_ids), plans and regions, in data",
			},
			{
				Type:        string(EventUserSSO),
				Description: "A one-time sign-in link for a customer account: metadata.path (and metadata.url when the panel knows its public address) lasts metadata.expires_in seconds and works once. Opening it signs the browser in and opens vm_id, or the dashboard; the panel still asks for its own two-factor code. Refused for staff, suspended, locked or expired accounts, and when vm_id belongs to someone else.",
				Required:    []string{"user_id"},
				Optional:    []string{"vm_id", "external_ref"},
			},
		},
		PayloadExample: map[string]interface{}{
			"event":     "vm.create",
			"timestamp": "2024-01-01T00:00:00Z",
			"nonce":     "5f2b9c0e7a1d4c3b",
			"data": map[string]interface{}{
				"user_id":     "panel user id",
				"hostname":    "client-vm-1",
				"template_id": "os template id",
				"vm_specs": map[string]interface{}{
					"cpu":    2,
					"memory": 4096,
					"disk":   50,
				},
				"external_ref": "billing_invoice_456",
			},
		},
	}

	return c.JSON(http.StatusOK, doc)
}

// ============================================================================
// Route Registration
// ============================================================================

// RegisterBillingRoutes registers all billing webhook routes
func RegisterBillingRoutes(e *echo.Echo, handler *BillingHandler) {
	// Webhook endpoint with authentication
	webhooks := e.Group("/webhooks")
	webhooks.Use(handler.RequireWebhookAuth())
	webhooks.POST("/billing", handler.HandleWebhook)

	// Documentation endpoint (public)
	e.GET("/webhooks/billing/docs", handler.GetWebhookDocumentation)
}
