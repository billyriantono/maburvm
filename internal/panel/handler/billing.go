// Package handler provides HTTP handlers for billing webhook operations
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
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
	// WebhookTimestampHeader is the header containing request timestamp
	WebhookTimestampHeader = "X-Webhook-Timestamp"

	// MaxWebhookAge is the maximum age of a webhook request (5 minutes)
	MaxWebhookAge = 5 * time.Minute
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
)

// IsValid checks if the event type is valid
func (e WebhookEventType) IsValid() bool {
	switch e {
	case EventVMCreate, EventVMSuspend, EventVMUnsuspend, EventVMDestroy,
		EventVMResize, EventVMResetPassword, EventVMUsage,
		EventVMStart, EventVMStop, EventVMRestart:
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
//	  "event": "vm.create|vm.suspend|vm.unsuspend|vm.destroy|vm.resize|vm.reset_password|vm.usage|vm.start|vm.stop|vm.restart",
//	  "timestamp": "2024-01-01T00:00:00Z",
//	  "data": {
//	    "user_id": "external_user_id",
//	    "vm_specs": {"cpu": 2, "memory": 4096, "disk": 50},
//	    "template_id": "ubuntu-22.04",
//	    "hostname": "client-vm-1"
//	  }
//	}
type WebhookPayload struct {
	Event     WebhookEventType `json:"event" validate:"required"`
	Timestamp time.Time        `json:"timestamp" validate:"required"`
	Data      WebhookData      `json:"data" validate:"required"`
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
	// ExternalRef is an optional external reference
	ExternalRef string `json:"external_ref,omitempty"`
	// Password is the new root password (vm.reset_password)
	Password string `json:"password,omitempty"`
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
}

// IdempotencyRecord stores the result of a processed webhook
type IdempotencyRecord struct {
	RequestID   string
	Response    WebhookResponse
	ProcessedAt time.Time
}

// ============================================================================
// BillingHandler
// ============================================================================

// BillingHandler handles billing webhook operations
type BillingHandler struct {
	vmService     *service.VMService
	logger        *slog.Logger
	webhookSecret string
	apiKey        string

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
		vmService:     vmService,
		logger:        logger,
		webhookSecret: getWebhookSecret(),
		apiKey:        getAPIKey(),
		db:            db,
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

// getWebhookSecret retrieves the webhook HMAC secret from the environment. It
// returns "" when unset — the webhook then fails closed (see RequireWebhookAuth)
// rather than falling back to a public default that would let anyone forge
// billing events (create/suspend/destroy VMs).
func getWebhookSecret() string {
	return os.Getenv("BILLING_WEBHOOK_SECRET")
}

// getAPIKey retrieves the billing API key from the environment. Empty when
// unset; the webhook fails closed rather than accepting a known default key.
func getAPIKey() string {
	return os.Getenv("BILLING_API_KEY")
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
			if h.apiKey == "" || h.webhookSecret == "" {
				h.logger.Error("billing webhook is disabled: BILLING_API_KEY and BILLING_WEBHOOK_SECRET must both be set")
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

			if apiKey != h.apiKey {
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

			// 2. Validate timestamp to prevent replay attacks
			timestampStr := c.Request().Header.Get(WebhookTimestampHeader)
			if timestampStr != "" {
				timestamp, err := time.Parse(time.RFC3339, timestampStr)
				if err == nil {
					age := time.Since(timestamp)
					if age < 0 {
						age = -age
					}
					if age > MaxWebhookAge {
						return c.JSON(http.StatusBadRequest, WebhookResponse{
							Success:   false,
							Message:   "Webhook request too old",
							Timestamp: time.Now().UTC(),
						})
					}
				}
			}

			// 3. Validate HMAC signature
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
			c.Request().Body = io.NopCloser(strings.NewReader(string(body)))

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

			return next(c)
		}
	}
}

// verifySignature verifies the HMAC-SHA256 signature of the webhook payload
func (h *BillingHandler) verifySignature(payload []byte, signature string) bool {
	// Remove "sha256=" prefix if present
	sig := strings.TrimPrefix(signature, "sha256=")

	// Compute HMAC
	mac := hmac.New(sha256.New, []byte(h.webhookSecret))
	mac.Write(payload)
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	// Constant-time comparison to prevent timing attacks
	return hmac.Equal([]byte(sig), []byte(expectedSig))
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
	if idempotencyKey != "" {
		if record, exists := h.checkIdempotency(c.Request().Context(), idempotencyKey); exists {
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
	}

	if err != nil {
		h.logger.Error("Webhook event processing failed",
			"event", payload.Event,
			"request_id", requestID,
			"error", err,
		)

		statusCode := http.StatusInternalServerError
		if errors.Is(err, service.ErrVMNotFound) {
			statusCode = http.StatusNotFound
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

// validatePayload validates the payload based on event type
func (h *BillingHandler) validatePayload(payload *WebhookPayload) error {
	switch payload.Event {
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
		if payload.Data.VMSpecs.CPU == 0 {
			return errors.New("vm_specs.cpu is required for vm.create")
		}
		if payload.Data.VMSpecs.Memory == 0 {
			return errors.New("vm_specs.memory is required for vm.create")
		}
		if payload.Data.VMSpecs.Disk == 0 {
			return errors.New("vm_specs.disk is required for vm.create")
		}

	default:
		if payload.Data.VMID == "" {
			return fmt.Errorf("vm_id is required for %s", payload.Event)
		}
		if payload.Event == EventVMResetPassword && len(payload.Data.Password) < 8 {
			return errors.New("password of at least 8 characters is required for vm.reset_password")
		}
		if sp := payload.Data.VMSpecs; payload.Event == EventVMResize && (sp.CPU < 1 || sp.Memory < 128 || sp.Disk < 1) {
			return errors.New("vm_specs.cpu, memory and disk are required for vm.resize")
		}
	}

	return nil
}

// ============================================================================
// Event Handlers
// ============================================================================

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

	// Map webhook specs to resources
	resources := models.Resources{
		CPU:  data.VMSpecs.CPU,
		RAM:  data.VMSpecs.Memory,
		Disk: data.VMSpecs.Disk,
	}

	// Create VM request
	createReq := &service.CreateVMRequest{
		UserID:       data.UserID,
		Hostname:     data.Hostname,
		OSTemplateID: data.TemplateID,
		Resources:    resources,
	}

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
// disk in the new package leaves the current one as it is.
func (h *BillingHandler) handleVMResize(ctx context.Context, data *WebhookData, requestID string) (WebhookResponse, error) {
	current, err := h.vmService.GetVM(ctx, data.VMID, false)
	if err != nil {
		return WebhookResponse{}, err
	}
	res := current.VM.Resources
	res.CPU, res.RAM = data.VMSpecs.CPU, data.VMSpecs.Memory
	if data.VMSpecs.Disk > res.Disk {
		res.Disk = data.VMSpecs.Disk
	}

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
	vm, err := h.vmService.UpdateVM(ctx, &service.UpdateVMRequest{VMID: data.VMID, Resources: &res, StartAfter: wasRunning})
	if err != nil {
		if wasRunning { // do not leave the customer's VM off over a refused resize
			_, _ = h.vmService.ExecuteLifecycleCommand(ctx, &service.LifecycleRequest{
				VMID: data.VMID, Command: service.LifecycleStart,
			})
		}
		return WebhookResponse{}, fmt.Errorf("failed to resize VM: %w", err)
	}
	return WebhookResponse{
		Success: true, Message: "VM resized", VMID: data.VMID, ExternalRef: data.ExternalRef,
		Timestamp: time.Now().UTC(), RequestID: requestID,
		Metadata: map[string]string{
			"cpu":    fmt.Sprintf("%d", vm.Resources.CPU),
			"memory": fmt.Sprintf("%d", vm.Resources.RAM),
			"disk":   fmt.Sprintf("%d", vm.Resources.Disk),
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
		},
		Events: []EventDocumentation{
			{
				Type:        string(EventVMCreate),
				Description: "Create a new VM with specified resources",
				Required:    []string{"user_id", "hostname", "template_id", "vm_specs.cpu", "vm_specs.memory", "vm_specs.disk"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMSuspend),
				Description: "Suspend a VM (power off; the owner cannot start it)",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMUnsuspend),
				Description: "Unsuspend a VM and start it",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMDestroy),
				Description: "Destroy a VM and cleanup resources",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMResize),
				Description: "Change CPU, memory and disk. A running VM is shut down and booted again. Disk only grows.",
				Required:    []string{"vm_id", "vm_specs.cpu", "vm_specs.memory", "vm_specs.disk"},
			},
			{
				Type:        string(EventVMResetPassword),
				Description: "Set the root password of a running VM",
				Required:    []string{"vm_id", "password"},
			},
			{
				Type:        string(EventVMUsage),
				Description: "Traffic used and allowed this period and the disk allowance, in MB, returned in metadata",
				Required:    []string{"vm_id"},
			},
			{
				Type:        "vm.start | vm.stop | vm.restart",
				Description: "Power actions",
				Required:    []string{"vm_id"},
			},
		},
		PayloadExample: map[string]interface{}{
			"event":     "vm.create",
			"timestamp": "2024-01-01T00:00:00Z",
			"data": map[string]interface{}{
				"user_id":     "external_user_123",
				"hostname":    "client-vm-1",
				"template_id": "ubuntu-22.04",
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
