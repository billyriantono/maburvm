package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// SensitiveFields contains field names that should be redacted from audit logs
	// to prevent sensitive data from being stored in plain text
	SensitiveFields = []string{
		"password",
		"password_hash",
		"token",
		"secret",
		"api_key",
		"private_key",
		"credential",
		"auth_token",
		"access_token",
		"refresh_token",
		"vnc_password",
		"two_factor_secret",
		"ssh_key_private",
		"api_secret",
		"session_token",
		"csrf_token",
	}

	// SkipPaths contains regex patterns for paths that should not be audited
	// These include authentication endpoints and health checks
	SkipPaths = []*regexp.Regexp{
		regexp.MustCompile(`^/auth/.*$`),
		regexp.MustCompile(`^/api/auth/.*$`),
		regexp.MustCompile(`^/health.*$`),
		regexp.MustCompile(`^/api/health.*$`),
		regexp.MustCompile(`^/ping.*$`),
		regexp.MustCompile(`^/ready.*$`),
		regexp.MustCompile(`^/live.*$`),
	}

	// MutationMethods contains HTTP methods that trigger audit logging
	MutationMethods = map[string]bool{
		http.MethodPost:   true,
		http.MethodPut:    true,
		http.MethodDelete: true,
		http.MethodPatch:  true,
	}
)

// Action represents an audit action type derived from HTTP method
type Action string

const (
	ActionCreate  Action = "CREATE"
	ActionUpdate  Action = "UPDATE"
	ActionDelete  Action = "DELETE"
	ActionPatch   Action = "PATCH"
	ActionUnknown Action = "UNKNOWN"
)

// ResourceType represents the type of resource being audited
type ResourceType string

const (
	ResourceVM           ResourceType = "VM"
	ResourceUser         ResourceType = "USER"
	ResourceNode         ResourceType = "NODE"
	ResourceNetwork      ResourceType = "NETWORK"
	ResourceFirewallRule ResourceType = "FIREWALL_RULE"
	ResourceSnapshot     ResourceType = "SNAPSHOT"
	ResourceBackup       ResourceType = "BACKUP"
	ResourceOSTemplate   ResourceType = "OS_TEMPLATE"
	ResourceSession      ResourceType = "SESSION"
	ResourceIPAddress    ResourceType = "IP_ADDRESS"
	ResourceUnknown      ResourceType = "UNKNOWN"
)

// String returns the string representation of ResourceType
func (rt ResourceType) String() string {
	return string(rt)
}

// AuditContext holds audit information during request processing
type AuditContext struct {
	UserID         *string
	Action         Action
	ResourceType   ResourceType
	ResourceID     *string
	IPAddress      string
	UserAgent      string
	Details        map[string]any
	BeforeSnapshot *map[string]any
	AfterSnapshot  *map[string]any
	Timestamp      time.Time
}

// Client wraps a queue client for audit operations
type Client struct {
	queueClient *queue.Client
}

// NewClient creates a new audit middleware client
func NewClient(queueClient *queue.Client) *Client {
	return &Client{
		queueClient: queueClient,
	}
}

// EchoMiddleware returns an Echo middleware that logs mutations to audit_logs
// via River queue asynchronously. It captures resource snapshots and redacts sensitive data.
//
// Features:
// - Logs only mutation methods (POST, PUT, DELETE, PATCH)
// - Skips authentication and health check endpoints
// - Extracts user ID from Echo context (set by auth middleware)
// - Captures request body as "after" snapshot with sensitive data redacted
// - Captures HTTP status code, method, and path in details
// - Enqueues audit log asynchronously (non-blocking)
// - Failed audit logging doesn't fail the request
func (c *Client) EchoMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ctx echo.Context) error {
			request := ctx.Request()

			// Skip non-mutation requests and excluded paths
			if shouldSkip(request) {
				return next(ctx)
			}

			// Create audit context
			auditCtx := &AuditContext{
				Action:       getAction(request.Method),
				ResourceType: getResourceType(request.URL.Path),
				IPAddress:    getClientIP(request),
				UserAgent:    request.UserAgent(),
				Details:      make(map[string]any),
				Timestamp:    time.Now().UTC(),
			}

			// Extract user ID from Echo context (set by auth middleware)
			extractUserIDFromContext(ctx, auditCtx)

			// Extract resource ID from path
			extractResourceID(auditCtx, request.URL.Path)

			// Capture request body for after snapshot
			captureRequestBody(request, auditCtx)

			// Use custom response writer to capture status code
			rec := &auditResponseRecorder{
				Response:   ctx.Response(),
				statusCode: http.StatusOK,
			}
			originalWriter := ctx.Response().Writer
			ctx.Response().Writer = rec

			// Process request
			err := next(ctx)

			// Restore original writer
			ctx.Response().Writer = originalWriter

			// Record additional details
			auditCtx.Details["http_status"] = rec.statusCode
			auditCtx.Details["method"] = request.Method
			auditCtx.Details["path"] = request.URL.Path
			if reqID := ctx.Response().Header().Get(echo.HeaderXRequestID); reqID != "" {
				auditCtx.Details["request_id"] = reqID
			}

			// Mark if the request failed
			if rec.statusCode >= 400 {
				auditCtx.Details["failed"] = true
				auditCtx.Details["status_class"] = "error"
			} else {
				auditCtx.Details["status_class"] = "success"
			}

			// Enqueue audit log asynchronously (don't block request)
			// Use goroutine to ensure non-blocking behavior
			go c.enqueueAuditLog(request.Context(), auditCtx)

			return err
		}
	}
}

// shouldSkip determines if the request should be skipped from audit logging
func shouldSkip(r *http.Request) bool {
	// Skip non-mutation methods (GET, HEAD, OPTIONS, TRACE, CONNECT)
	if !MutationMethods[r.Method] {
		return true
	}

	// Skip excluded paths (auth, health checks)
	path := r.URL.Path
	for _, pattern := range SkipPaths {
		if pattern.MatchString(path) {
			return true
		}
	}

	return false
}

// getAction converts HTTP method to audit action
func getAction(method string) Action {
	switch method {
	case http.MethodPost:
		return ActionCreate
	case http.MethodPut:
		return ActionUpdate
	case http.MethodDelete:
		return ActionDelete
	case http.MethodPatch:
		return ActionPatch
	default:
		return ActionUnknown
	}
}

// getResourceType extracts resource type from URL path
func getResourceType(path string) ResourceType {
	path = strings.ToLower(path)

	// More specific matches first to avoid misclassification
	switch {
	case strings.Contains(path, "/firewall"):
		return ResourceFirewallRule
	case strings.Contains(path, "/snapshots"):
		return ResourceSnapshot
	case strings.Contains(path, "/backups"):
		return ResourceBackup
	case strings.Contains(path, "/templates"):
		return ResourceOSTemplate
	case strings.Contains(path, "/sessions"):
		return ResourceSession
	case strings.Contains(path, "/ip-addresses"):
		return ResourceIPAddress
	case strings.Contains(path, "/networks"):
		return ResourceNetwork
	case strings.Contains(path, "/vms"):
		return ResourceVM
	case strings.Contains(path, "/users"):
		return ResourceUser
	case strings.Contains(path, "/nodes"):
		return ResourceNode
	default:
		return ResourceUnknown
	}
}

// extractResourceID extracts the resource ID from the URL path
func extractResourceID(ctx *AuditContext, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")

	// Look for UUID patterns or numeric IDs in path segments
	for _, part := range parts {
		// Skip API version prefix and common non-ID segments
		if part == "api" || part == "v1" || part == "v2" || part == "v3" {
			continue
		}

		// Skip resource type segments (match against known resource types)
		lowerPart := strings.ToLower(part)
		if isResourceTypeName(lowerPart) {
			continue
		}

		// Check if this segment looks like an ID (UUID or numeric)
		if isResourceID(part) {
			ctx.ResourceID = &part
			return
		}
	}
}

// isResourceTypeName checks if a segment is a resource type name (not an ID)
func isResourceTypeName(segment string) bool {
	resourceTypes := []string{
		"vms", "users", "nodes", "networks", "firewall",
		"snapshots", "backups", "templates", "sessions",
		"ip-addresses", "auth", "health", "api",
	}
	for _, rt := range resourceTypes {
		if segment == rt {
			return true
		}
	}
	return false
}

// isResourceID checks if a string segment looks like a resource ID
func isResourceID(segment string) bool {
	// UUID pattern (standard UUID format)
	if matched, _ := regexp.MatchString(
		`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`,
		segment,
	); matched {
		return true
	}
	// Numeric ID (positive integer)
	if matched, _ := regexp.MatchString(`^\d+$`, segment); matched {
		return true
	}
	return false
}

// extractUserIDFromContext extracts user ID from Echo context
func extractUserIDFromContext(ctx echo.Context, auditCtx *AuditContext) {
	// Try to get user context from Echo context (set by auth middleware)
	if user, ok := GetUserContext(ctx); ok && user != nil {
		userID := user.ID.String()
		auditCtx.UserID = &userID
		return
	}

	// Fallback to header (for API key auth or testing)
	request := ctx.Request()
	if userID := request.Header.Get("X-User-ID"); userID != "" {
		auditCtx.UserID = &userID
	}
}

// captureRequestBody reads the request body and creates an after snapshot
func captureRequestBody(r *http.Request, auditCtx *AuditContext) {
	if r.Body == nil || r.Body == http.NoBody {
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil || len(bodyBytes) == 0 {
		return
	}

	// Restore body for subsequent handlers
	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))

	// Parse and redact body
	var requestBody map[string]any
	if err := json.Unmarshal(bodyBytes, &requestBody); err == nil {
		auditCtx.AfterSnapshot = redactSnapshot(&requestBody)
	}
}

// getClientIP extracts the client IP from request headers or RemoteAddr
func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header (common for proxies/load balancers)
	xff := r.Header.Get("X-Forwarded-For")
	if xff != "" {
		ips := strings.Split(xff, ",")
		if len(ips) > 0 {
			return strings.TrimSpace(ips[0])
		}
	}

	// Check X-Real-Ip header (alternative proxy header)
	xri := r.Header.Get("X-Real-Ip")
	if xri != "" {
		return xri
	}

	// Check CF-Connecting-IP (Cloudflare)
	cfIP := r.Header.Get("CF-Connecting-IP")
	if cfIP != "" {
		return cfIP
	}

	// Fallback to RemoteAddr (strip port if present)
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	// Handle IPv6 addresses with brackets
	ip = strings.Trim(ip, "[]")
	return ip
}

// redactSnapshot recursively redacts sensitive fields from a snapshot
// It traverses nested maps and arrays to ensure all sensitive data is redacted
func redactSnapshot(snapshot *map[string]any) *map[string]any {
	if snapshot == nil {
		return nil
	}

	redacted := make(map[string]any)
	for k, v := range *snapshot {
		if isSensitiveField(k) {
			redacted[k] = "[REDACTED]"
		} else if nested, ok := v.(map[string]any); ok {
			redacted[k] = *redactSnapshot(&nested)
		} else if arr, ok := v.([]any); ok {
			redacted[k] = redactArray(arr)
		} else {
			redacted[k] = v
		}
	}
	return &redacted
}

// redactArray recursively redacts sensitive fields from array elements
func redactArray(arr []any) []any {
	result := make([]any, len(arr))
	for i, v := range arr {
		if nested, ok := v.(map[string]any); ok {
			result[i] = *redactSnapshot(&nested)
		} else {
			result[i] = v
		}
	}
	return result
}

// isSensitiveField checks if a field name contains sensitive keywords
func isSensitiveField(field string) bool {
	lower := strings.ToLower(field)
	for _, sensitive := range SensitiveFields {
		if lower == sensitive || strings.Contains(lower, sensitive) {
			return true
		}
	}
	return false
}

// enqueueAuditLog sends the audit log to the River queue asynchronously
// This function is designed to be non-blocking - errors are logged but not returned
func (c *Client) enqueueAuditLog(ctx context.Context, auditCtx *AuditContext) {
	if c.queueClient == nil {
		return
	}

	job := queue.AuditJob{
		UserID:         auditCtx.UserID,
		Action:         string(auditCtx.Action),
		ResourceType:   string(auditCtx.ResourceType),
		ResourceID:     auditCtx.ResourceID,
		IPAddress:      auditCtx.IPAddress,
		UserAgent:      auditCtx.UserAgent,
		Details:        auditCtx.Details,
		BeforeSnapshot: auditCtx.BeforeSnapshot,
		AfterSnapshot:  auditCtx.AfterSnapshot,
		Timestamp:      auditCtx.Timestamp,
	}

	// Insert job to River queue
	// Errors are intentionally not returned to prevent blocking the request
	// The AuditWorker handles retries and logging of failures
	_, _ = c.queueClient.InsertAudit(ctx, job)
}

// auditResponseRecorder wraps echo.Response to capture status code
type auditResponseRecorder struct {
	*echo.Response
	statusCode int
}

// WriteHeader captures the status code before writing
func (rec *auditResponseRecorder) WriteHeader(code int) {
	rec.statusCode = code
	rec.Response.WriteHeader(code)
}

// Write captures the written bytes and delegates to the underlying response
func (rec *auditResponseRecorder) Write(b []byte) (int, error) {
	return rec.Response.Write(b)
}

// AuditLog manually creates an audit log entry for programmatic use
// This can be called from services when automatic middleware logging is not sufficient
// Examples: background jobs, bulk operations, or complex multi-step transactions
func (c *Client) AuditLog(
	userID *string,
	action string,
	resourceType string,
	resourceID *string,
	ipAddress string,
	userAgent string,
	details map[string]any,
	beforeSnapshot *map[string]any,
	afterSnapshot *map[string]any,
) {
	if c.queueClient == nil {
		return
	}

	// Redact sensitive data from snapshots before logging
	if beforeSnapshot != nil {
		beforeSnapshot = redactSnapshot(beforeSnapshot)
	}
	if afterSnapshot != nil {
		afterSnapshot = redactSnapshot(afterSnapshot)
	}

	job := queue.AuditJob{
		UserID:         userID,
		Action:         action,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		IPAddress:      ipAddress,
		UserAgent:      userAgent,
		Details:        details,
		BeforeSnapshot: beforeSnapshot,
		AfterSnapshot:  afterSnapshot,
		Timestamp:      time.Now().UTC(),
	}

	// Fire and forget - don't block the caller
	// Use background context as this is fire-and-forget
	go func() {
		_, _ = c.queueClient.InsertAudit(context.Background(), job)
	}()
}

// CreateAuditContext creates a new AuditContext for manual audit logging
// This is useful when you need to capture before/after snapshots programmatically
func CreateAuditContext(userID *string, action Action, resourceType ResourceType) *AuditContext {
	return &AuditContext{
		UserID:       userID,
		Action:       action,
		ResourceType: resourceType,
		Details:      make(map[string]any),
		Timestamp:    time.Now().UTC(),
	}
}

// SetBeforeSnapshot sets the before snapshot in the audit context with redaction
func (a *AuditContext) SetBeforeSnapshot(data map[string]any) {
	a.BeforeSnapshot = redactSnapshot(&data)
}

// SetAfterSnapshot sets the after snapshot in the audit context with redaction
func (a *AuditContext) SetAfterSnapshot(data map[string]any) {
	a.AfterSnapshot = redactSnapshot(&data)
}

// SetDetail adds a detail to the audit context
func (a *AuditContext) SetDetail(key string, value any) {
	if a.Details == nil {
		a.Details = make(map[string]any)
	}
	a.Details[key] = value
}
