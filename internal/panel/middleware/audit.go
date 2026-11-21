package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
)

var (
	// sensitiveSubstrings: a body field whose name contains one of these is
	// stored as [REDACTED]. Over-redacting costs a detail; under-redacting
	// writes a credential into a table every administrator can read.
	sensitiveSubstrings = []string{
		"pass", "secret", "token", "private", "credential", "api_key", "apikey",
		"access_key", "accesskey", "totp", "recovery", "backup_code", "signature",
		"authorization", "cookie", "ticket", "user_data", "userdata", "cloud_init", "cloudinit",
		// A chat webhook URL is itself the credential (alerts keep it encrypted).
		"webhook_url", "webhookurl",
	}
	// sensitiveExact are names too short to match as substrings.
	sensitiveExact = map[string]bool{"code": true, "otp": true, "key": true, "pin": true}

	// skipPaths are mutating requests that change nothing worth recording.
	skipPaths = []*regexp.Regexp{
		regexp.MustCompile(`^/(api/v1/)?(health|healthz|livez|readyz|ready|ping)`),
		regexp.MustCompile(`/preview$`),
	}

	uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

const (
	// maxAuditBody bounds the JSON body kept as the after snapshot.
	maxAuditBody = 64 << 10
	// maxAuditString shortens long values (a logo as a data URL, a script).
	maxAuditString = 512
	skipAuditKey   = "audit.skip"
)

// resourceTypes maps a path segment to the resource it names, most specific
// first: /vms/:id/firewall is a firewall rule, not a VM.
var resourceTypes = []struct{ seg, rt string }{
	{"/auth/", "SESSION"},
	{"/webhooks/billing", "BILLING"},
	{"/mcp", "MCP"},
	{"/api-keys", "API_KEY"},
	{"/support-access", "SUPPORT_ACCESS"},
	{"/firewall", "FIREWALL_RULE"},
	{"/port-forwards", "PORT_FORWARD"},
	{"/snapshots", "SNAPSHOT"},
	{"/backups", "BACKUP"},
	{"/floating-ips", "FLOATING_IP"},
	{"/ip-addresses", "IP_ADDRESS"},
	{"/ipam", "IP_POOL"},
	{"/ip-pools", "IP_POOL"},
	{"/templates", "OS_TEMPLATE"},
	{"/images", "IMAGE"},
	{"/isos", "ISO"},
	{"/settings", "SETTINGS"},
	{"/alerts", "ALERT"},
	{"/plans", "PLAN"},
	{"/regions", "REGION"},
	{"/storage", "STORAGE"},
	{"/dns", "DNS"},
	{"/vpcs", "VPC"},
	{"/networks", "NETWORK"},
	{"/users", "USER"},
	{"/nodes", "NODE"},
	{"/vms", "VM"},
}

// Client records mutating requests to audit_logs through the job queue.
type Client struct {
	insert func(context.Context, queue.AuditJob) error
}

// NewClient creates the audit middleware client. A nil queue records nothing.
func NewClient(queueClient *queue.Client) *Client {
	if queueClient == nil {
		return &Client{}
	}
	return &Client{insert: func(ctx context.Context, job queue.AuditJob) error {
		_, err := queueClient.InsertAudit(ctx, job)
		return err
	}}
}

// SkipAudit marks a request whose POST only reads (a billing status poll), so
// it is not recorded.
func SkipAudit(c echo.Context) { c.Set(skipAuditKey, true) }

// EchoMiddleware records every POST, PUT, PATCH and DELETE: who, from where,
// which route, the outcome, and the JSON body with credentials redacted.
// Mounted globally; it reads the user after the route's auth has run. A request
// whose handler already wrote its own audit row is not recorded twice.
func (c *Client) EchoMiddleware() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(ctx echo.Context) error {
			req := ctx.Request()
			if c.insert == nil || shouldSkip(req) {
				return next(ctx)
			}
			job := queue.AuditJob{
				Action:       auditAction(req),
				ResourceType: resourceType(req.URL.Path),
				ResourceID:   resourceID(req.URL.Path),
				IPAddress:    ctx.RealIP(),
				UserAgent:    truncate(req.UserAgent(), 500),
				Timestamp:    time.Now().UTC(),
			}
			job.AfterSnapshot = captureBody(req)

			trackCtx, written := models.TrackAuditWrites(req.Context())
			ctx.SetRequest(req.WithContext(trackCtx))
			err := next(ctx)
			ctx.SetRequest(req)

			if written.Load() || ctx.Get(skipAuditKey) != nil {
				return err
			}
			status := ctx.Response().Status
			if err != nil && !ctx.Response().Committed {
				status = http.StatusInternalServerError
				var he *echo.HTTPError
				if errors.As(err, &he) {
					status = he.Code
				}
			}
			// Nothing changed: no such route, or refused by a rate limit (a
			// flood must not turn into a flood of audit rows).
			if ctx.Path() == "" || status == http.StatusMethodNotAllowed || status == http.StatusTooManyRequests {
				return err
			}
			user, signedIn := GetUserContext(ctx)
			// Anyone could fill the table with refused anonymous calls; failed
			// sign-ins (under /auth/, rate limited) are still kept.
			if !signedIn && status == http.StatusUnauthorized && !strings.Contains(req.URL.Path, "/auth/") {
				return err
			}
			if signedIn {
				id := user.ID.String()
				job.UserID = &id
			}
			job.Details = map[string]any{
				"method":      req.Method,
				"path":        req.URL.Path,
				"route":       ctx.Path(),
				"http_status": status,
				"failed":      status >= 400,
			}
			if signedIn && user.ImpersonatorID != "" {
				job.Details["impersonator_id"], job.Details["impersonator_email"] = user.ImpersonatorID, user.ImpersonatorEmail
			}
			if rid := ctx.Response().Header().Get(echo.HeaderXRequestID); rid != "" {
				job.Details["request_id"] = rid
			}
			if loc := ctx.Response().Header().Get(echo.HeaderLocation); loc != "" {
				loc, _, _ = strings.Cut(loc, "#") // an SSO two-factor ticket rides in the fragment
				job.Details["redirect"] = loc
			}
			// After the response: the request context is done by then.
			go func() {
				ictx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = c.insert(ictx, job)
			}()
			return err
		}
	}
}

func shouldSkip(r *http.Request) bool {
	switch {
	case r.Method == http.MethodPost, r.Method == http.MethodPut, r.Method == http.MethodPatch, r.Method == http.MethodDelete:
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/auth/oidc/callback"):
		// An SSO sign-in finishes on a GET; its outcome is where it redirects to.
	default:
		return true
	}
	for _, p := range skipPaths {
		if p.MatchString(r.URL.Path) {
			return true
		}
	}
	return false
}

func auditAction(r *http.Request) string {
	switch {
	case strings.HasSuffix(r.URL.Path, "/auth/login"), strings.HasSuffix(r.URL.Path, "/auth/oidc/2fa"),
		strings.HasSuffix(r.URL.Path, "/auth/oidc/callback"):
		return "LOGIN"
	case strings.HasSuffix(r.URL.Path, "/auth/logout"):
		return "LOGOUT"
	}
	switch r.Method {
	case http.MethodPost:
		return "CREATE"
	case http.MethodPut:
		return "UPDATE"
	case http.MethodPatch:
		return "PATCH"
	default:
		return "DELETE"
	}
}

func resourceType(path string) string {
	path = strings.ToLower(path)
	for _, r := range resourceTypes {
		if strings.Contains(path, r.seg) {
			return r.rt
		}
	}
	return "UNKNOWN"
}

// resourceID is the first UUID in the path. audit_logs.resource_id is a uuid
// column, so anything else would fail the insert.
func resourceID(path string) *string {
	for _, part := range strings.Split(path, "/") {
		if uuidRe.MatchString(part) {
			return &part
		}
	}
	return nil
}

// captureBody reads a JSON body (and puts it back) as the redacted after
// snapshot. Uploads and other bodies are not read.
func captureBody(r *http.Request) *map[string]any {
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength > maxAuditBody ||
		!strings.HasPrefix(r.Header.Get(echo.HeaderContentType), echo.MIMEApplicationJSON) {
		return nil
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAuditBody+1))
	r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), r.Body))
	if err != nil || len(raw) == 0 || len(raw) > maxAuditBody {
		return nil
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return nil
	}
	out := redactMap(body)
	return &out
}

func redactMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if isSensitiveField(k) {
			out[k] = "[REDACTED]"
			continue
		}
		out[k] = redactValue(v)
	}
	return out
}

func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		return redactMap(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = redactValue(e)
		}
		return out
	case string:
		return truncate(t, maxAuditString)
	}
	return v
}

func isSensitiveField(field string) bool {
	lower := strings.ToLower(field)
	if sensitiveExact[lower] {
		return true
	}
	for _, s := range sensitiveSubstrings {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
