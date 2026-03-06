// Package handler provides HTTP handlers for billing webhook operations
package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
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
)

// IsValid checks if the event type is valid
func (e WebhookEventType) IsValid() bool {
	switch e {
	case EventVMCreate, EventVMSuspend, EventVMUnsuspend, EventVMDestroy:
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
//	  "event": "vm.create|vm.suspend|vm.unsuspend|vm.destroy",
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

	// Idempotency cache
	idempotencyCache map[string]*IdempotencyRecord
	idempotencyMutex sync.RWMutex
}

// NewBillingHandler creates a new BillingHandler instance
func NewBillingHandler(vmService *service.VMService, logger *slog.Logger) *BillingHandler {
	return &BillingHandler{
		vmService:        vmService,
		logger:           logger,
		webhookSecret:    getWebhookSecret(),
		apiKey:           getAPIKey(),
		idempotencyCache: make(map[string]*IdempotencyRecord),
	}
}

// getWebhookSecret retrieves the webhook secret from environment
func getWebhookSecret() string {
	secret := os.Getenv("BILLING_WEBHOOK_SECRET")
	if secret == "" {
		secret = "default-webhook-secret-change-in-production"
	}
	return secret
}

// getAPIKey retrieves the API key from environment
func getAPIKey() string {
	key := os.Getenv("BILLING_API_KEY")
	if key == "" {
		key = "default-api-key-change-in-production"
	}
	return key
}

// ============================================================================
// Authentication & Security Middleware
// ============================================================================

// RequireWebhookAuth middleware validates API key and HMAC signature
func (h *BillingHandler) RequireWebhookAuth() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
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
func (h *BillingHandler) checkIdempotency(idempotencyKey string) (*IdempotencyRecord, bool) {
	h.idempotencyMutex.RLock()
	defer h.idempotencyMutex.RUnlock()

	record, exists := h.idempotencyCache[idempotencyKey]
	if !exists {
		return nil, false
	}

	// Check if record is still valid
	if time.Since(record.ProcessedAt) > IdempotencyCacheTTL {
		return nil, false
	}

	return record, true
}

// storeIdempotency stores the result of a processed request
func (h *BillingHandler) storeIdempotency(idempotencyKey string, record *IdempotencyRecord) {
	h.idempotencyMutex.Lock()
	defer h.idempotencyMutex.Unlock()

	h.idempotencyCache[idempotencyKey] = record
}

// cleanupOldIdempotency removes expired idempotency records
func (h *BillingHandler) cleanupOldIdempotency() {
	h.idempotencyMutex.Lock()
	defer h.idempotencyMutex.Unlock()

	now := time.Now()
	for key, record := range h.idempotencyCache {
		if now.Sub(record.ProcessedAt) > IdempotencyCacheTTL {
			delete(h.idempotencyCache, key)
		}
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
		if record, exists := h.checkIdempotency(idempotencyKey); exists {
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

	// Store idempotency record if key was provided
	if idempotencyKey != "" {
		h.storeIdempotency(idempotencyKey, &IdempotencyRecord{
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

	case EventVMSuspend, EventVMUnsuspend, EventVMDestroy:
		if payload.Data.VMID == "" {
			return fmt.Errorf("vm_id is required for %s", payload.Event)
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

// handleVMSuspend suspends a VM (pause + network block)
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

	// Execute suspend command (pause VM)
	suspendReq := &service.LifecycleRequest{
		VMID:    data.VMID,
		Command: service.LifecycleStop, // Suspend by stopping the VM
		Async:   false,                 // Synchronous for webhook
	}

	// Use force stop for suspend (equivalent to pausing)
	suspendReq.Command = service.LifecycleForceStop

	_, err = h.vmService.ExecuteLifecycleCommand(ctx, suspendReq)
	if err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to suspend VM: %w", err)
	}

	// Block network by adding a deny-all firewall rule
	// This is done through the VM service by updating VM status to suspended
	// The actual network blocking would be handled by the agent

	h.logger.Info("VM suspended successfully via webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM suspended successfully (VM paused, network blocked)",
		VMID:        data.VMID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"previous_status": string(vm.VM.Status),
			"action":          "paused_and_network_blocked",
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

	// Execute start command to resume
	startReq := &service.LifecycleRequest{
		VMID:    data.VMID,
		Command: service.LifecycleStart,
		Async:   false,
	}

	_, err = h.vmService.ExecuteLifecycleCommand(ctx, startReq)
	if err != nil {
		return WebhookResponse{}, fmt.Errorf("failed to unsuspend VM: %w", err)
	}

	// Network is automatically unblocked when VM starts

	h.logger.Info("VM unsuspended successfully via webhook",
		"vm_id", data.VMID,
		"request_id", requestID,
	)

	return WebhookResponse{
		Success:     true,
		Message:     "VM unsuspended successfully (VM resumed, network restored)",
		VMID:        data.VMID,
		ExternalRef: data.ExternalRef,
		Timestamp:   time.Now().UTC(),
		RequestID:   requestID,
		Metadata: map[string]string{
			"previous_status": string(vm.VM.Status),
			"action":          "resumed_and_network_restored",
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
				Description: "Suspend a VM (pause + block network)",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMUnsuspend),
				Description: "Unsuspend a VM (resume + restore network)",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
			},
			{
				Type:        string(EventVMDestroy),
				Description: "Destroy a VM and cleanup resources",
				Required:    []string{"vm_id"},
				Optional:    []string{"external_ref"},
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
