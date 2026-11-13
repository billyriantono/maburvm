package handler

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/riverqueue/river"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/queue"
)

// Operator tools that are all "ask the database, show the answer": the
// security centre, temporary support access, the setup checklist, data export
// and the job queue. Admin only.

// SecurityCheck is one finding in the security centre. Status is "ok", "warn"
// or "fail"; Fix names the page that resolves it.
type SecurityCheck struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func countRows(db *gorm.DB, query string, args ...interface{}) int64 {
	var n int64
	_ = db.Raw(query, args...).Scan(&n).Error
	return n
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

// securityChecks inspects the live install. Every line is a fact read just
// now, not advice: a check that cannot be measured is left out.
func securityChecks(ctx context.Context, db *gorm.DB) []SecurityCheck {
	db = db.WithContext(ctx)
	var out []SecurityCheck
	add := func(id, title string, bad bool, badStatus, okDetail, badDetail, fix string) {
		c := SecurityCheck{ID: id, Title: title, Status: "ok", Detail: okDetail}
		if bad {
			c.Status, c.Detail, c.Fix = badStatus, badDetail, fix
		}
		out = append(out, c)
	}

	admins := countRows(db, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL AND expires_at IS NULL")
	no2fa := countRows(db, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND deleted_at IS NULL AND expires_at IS NULL AND two_factor_enabled = false")
	add("admin_2fa", "Two-step sign-in for administrators", no2fa > 0, "fail",
		fmt.Sprintf("All %s use two-step sign-in.", plural(admins, "administrator", "administrators")),
		fmt.Sprintf("%s can sign in with a password alone. A stolen password is then full control of every VM.", plural(no2fa, "administrator", "administrators")),
		"/settings/profile")

	publicURL := strings.TrimSpace(os.Getenv("PANEL_PUBLIC_URL"))
	switch {
	case publicURL == "":
		add("public_url", "Panel address", true, "warn", "", "The panel does not know its public address (PANEL_PUBLIC_URL is not set), so links in emails and logos in emails are left out.", "")
	case strings.HasPrefix(publicURL, "http://"):
		add("public_url", "Panel address", true, "fail", "", "The panel's public address is plain http. Passwords and sessions travel unencrypted. Serve it over https and update PANEL_PUBLIC_URL.", "")
	default:
		add("public_url", "Panel address", false, "", "Served over https at "+publicURL+".", "", "")
	}

	_, smtpReady, _ := service.LoadSMTPSettings(db)
	add("smtp", "Outgoing email", !smtpReady, "warn",
		"A mail server is set, so password resets and welcome emails can be sent.",
		"No mail server is set. Nobody can reset a forgotten password by email.", "/settings/system")

	var smtpPass string
	_ = db.Raw("SELECT COALESCE(data->>'smtpPassword', '') FROM system_settings WHERE section = 'email'").Scan(&smtpPass).Error
	add("secrets_at_rest", "Stored mail password", smtpPass != "", "warn",
		"No mail password is stored.",
		"The mail server password is stored in the database as it was typed, not encrypted. Anyone with a database backup can read it. Use an app-specific password that can do nothing but send mail.", "/settings/system")

	offline := countRows(db, "SELECT COUNT(*) FROM nodes WHERE status = 'offline' AND deleted_at IS NULL")
	add("nodes_offline", "Nodes reachable", offline > 0, "warn",
		"Every node is answering.",
		fmt.Sprintf("%s not answering. Their VMs cannot be managed and firewall changes are not reaching them.", plural(offline, "node is", "nodes are")), "/nodes")

	keys := countRows(db, "SELECT COUNT(*) FROM api_keys k JOIN users u ON u.id = k.user_id WHERE k.deleted_at IS NULL AND k.expires_at IS NULL AND k.read_only = false AND u.role = 'admin' AND COALESCE(k.role, '') <> 'client'")
	add("api_keys", "Administrator API keys", keys > 0, "warn",
		"No administrator API key is both unlimited and without an end date.",
		fmt.Sprintf("%s full administrator access and never expire. Give keys an end date, or make them read-only where that is enough.", plural(keys, "API key has", "API keys have")), "/settings/api-keys")

	support := countRows(db, "SELECT COUNT(*) FROM users WHERE expires_at > NOW() AND deleted_at IS NULL")
	add("support_access", "Temporary support access", support > 0, "warn",
		"No temporary support account is open.",
		fmt.Sprintf("%s open right now. End it as soon as the work is done.", plural(support, "temporary support account is", "temporary support accounts are")), "/security")

	billingKey, billingSecret := os.Getenv("BILLING_API_KEY"), os.Getenv("BILLING_WEBHOOK_SECRET")
	switch {
	case billingKey == "" && billingSecret == "":
		add("billing", "Billing webhook", false, "", "Off. No billing system can create or remove VMs.", "", "")
	case len(billingKey) < 24 || len(billingSecret) < 24:
		add("billing", "Billing webhook", true, "fail", "", "The billing API key or signing secret is shorter than 24 characters. Whoever guesses it can create and destroy VMs. Replace both with long random values.", "")
	default:
		add("billing", "Billing webhook", false, "", "On, with a key and signing secret of reasonable length.", "", "")
	}
	return out
}

// SupportGrant is one open temporary account.
type SupportGrant struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Note      string    `json:"note"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

// SetupStep is one line of the first-run checklist.
type SetupStep struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Why   string `json:"why"`
	Href  string `json:"href"`
	Done  bool   `json:"done"`
}

func setupSteps(ctx context.Context, db *gorm.DB, userID string) []SetupStep {
	db = db.WithContext(ctx)
	has := func(q string, args ...interface{}) bool { return countRows(db, q, args...) > 0 }
	_, smtpReady, _ := service.LoadSMTPSettings(db)
	return []SetupStep{
		{"node", "Connect a node", "A node is the server your VMs run on. Nothing can be created without one.", "/nodes",
			has("SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL")},
		{"ip_pool", "Add an IP range", "New VMs take their address from a range you own.", "/ip-pools",
			has("SELECT COUNT(*) FROM ip_pools WHERE deleted_at IS NULL")},
		{"template", "Add an operating system", "Templates are the systems customers can install.", "/templates",
			has("SELECT COUNT(*) FROM os_templates WHERE is_active = true")},
		{"plan", "Create a plan", "A plan is a size (CPU, memory, disk) customers can order.", "/plans",
			has("SELECT COUNT(*) FROM plans WHERE deleted_at IS NULL")},
		{"email", "Set up outgoing email", "Needed for password resets and welcome emails.", "/settings/system", smtpReady},
		{"branding", "Put your name on it", "Your name, logo and colour on the panel and the sign-in page.", "/settings/branding",
			has("SELECT COUNT(*) FROM system_settings WHERE section = ?", service.BrandingSection)},
		{"two_factor", "Turn on two-step sign-in", "Protects your administrator account if the password leaks.", "/settings/profile",
			has("SELECT COUNT(*) FROM users WHERE id = ? AND two_factor_enabled = true", userID)},
	}
}

// csvCell stops a spreadsheet from running a cell as a formula: a value that
// starts with = + - or @ gets a leading apostrophe.
func csvCell(v interface{}) string {
	var s string
	switch t := v.(type) {
	case nil:
		return ""
	case []byte:
		s = string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	default:
		s = fmt.Sprint(t)
	}
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// exports maps an export name to its query. Column aliases become the header.
var exports = map[string]string{
	"vms": `SELECT v.id, v.hostname, v.status, v.suspended, u.email AS owner, n.name AS node,
		v.resources->>'cpu' AS cpu, v.resources->>'ram' AS memory_mb, v.resources->>'disk' AS disk_gb,
		(SELECT string_agg(host(nw.ip_address), ' ') FROM networks nw WHERE nw.vm_id = v.id AND nw.deleted_at IS NULL) AS ip_addresses,
		v.created_at
		FROM vms v LEFT JOIN users u ON u.id = v.user_id LEFT JOIN nodes n ON n.id = v.node_id
		WHERE v.deleted_at IS NULL ORDER BY v.created_at`,
	"users": `SELECT u.id, u.email, u.name, u.role, u.two_factor_enabled,
		(SELECT COUNT(*) FROM vms v WHERE v.user_id = u.id AND v.deleted_at IS NULL) AS vms, u.created_at
		FROM users u WHERE u.deleted_at IS NULL AND u.expires_at IS NULL ORDER BY u.created_at`,
	"ip-addresses": `SELECT host(nw.ip_address) AS ip_address, v.hostname AS vm, u.email AS owner, n.name AS node, nw.created_at AS assigned_at
		FROM networks nw JOIN vms v ON v.id = nw.vm_id LEFT JOIN users u ON u.id = v.user_id LEFT JOIN nodes n ON n.id = v.node_id
		WHERE nw.deleted_at IS NULL AND v.deleted_at IS NULL ORDER BY nw.ip_address`,
	"audit-log": `SELECT a.created_at, u.email AS "user", a.action, a.resource_type, a.resource_id, host(a.ip_address) AS ip_address
		FROM audit_logs a LEFT JOIN users u ON u.id = a.user_id ORDER BY a.created_at DESC LIMIT 100000`,
}

// QueueJob is one background job as the queue page lists it.
type QueueJob struct {
	ID          int64      `json:"id"`
	Kind        string     `json:"kind"`
	Queue       string     `json:"queue"`
	State       string     `json:"state"`
	Attempt     int        `json:"attempt"`
	MaxAttempts int        `json:"max_attempts"`
	CreatedAt   time.Time  `json:"created_at"`
	FinalizedAt *time.Time `json:"finalized_at,omitempty"`
	// Subject says what the job is about (a VM operation, a backup), read from its arguments.
	Subject   string `json:"subject"`
	LastError string `json:"last_error"`
}

// queueWorkerLimits is what an operator may set per queue. Applied when the
// panel starts; see queue.ApplyWorkerOverrides.
var queueWorkerLimits = []string{queue.QueueCritical, queue.QueueDefault, queue.QueueBatch, queue.QueueAudit}

// RegisterOpsRoutes wires the operator tools under /api/v1.
func RegisterOpsRoutes(e *echo.Echo, db *gorm.DB, riverClient *river.Client[pgx.Tx], running *queue.Config) {
	if running == nil {
		running = queue.DefaultConfig("")
	}
	g := e.Group("/api/v1", middleware.RequireAuth(db), middleware.RequirePermission("admin:access"))
	bad := func(c echo.Context, msg string) error {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": msg})
	}
	ok := func(c echo.Context, data interface{}) error {
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": data})
	}

	// --- Security centre ---
	g.GET("/security/checks", func(c echo.Context) error {
		return ok(c, securityChecks(c.Request().Context(), db))
	})

	// --- Temporary support access ---
	g.GET("/security/support-access", func(c echo.Context) error {
		grants := []SupportGrant{}
		if err := db.WithContext(c.Request().Context()).
			Raw("SELECT id, email, support_note AS note, expires_at, created_at FROM users WHERE expires_at > NOW() AND deleted_at IS NULL ORDER BY expires_at").
			Scan(&grants).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not list support access"})
		}
		return ok(c, grants)
	})
	g.POST("/security/support-access", func(c echo.Context) error {
		var req struct {
			Hours int    `json:"hours"`
			Note  string `json:"note"`
		}
		if err := c.Bind(&req); err != nil {
			return bad(c, "Invalid request body")
		}
		req.Note = strings.TrimSpace(req.Note)
		if req.Hours < 1 || req.Hours > 168 {
			return bad(c, "Choose between 1 hour and 7 days")
		}
		if req.Note == "" || len([]rune(req.Note)) > 200 {
			return bad(c, "Say who this is for and why, in up to 200 characters")
		}
		// The account name and the password come from separate random bytes:
		// the name is shown on screen and must say nothing about the password.
		raw, tag := make([]byte, 18), make([]byte, 4)
		if _, err := rand.Read(raw); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not create a password"})
		}
		if _, err := rand.Read(tag); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not create a password"})
		}
		password := hex.EncodeToString(raw)
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not create a password"})
		}
		grant := SupportGrant{
			ID:        uuid.New().String(),
			Email:     "support-" + hex.EncodeToString(tag) + "@support.invalid",
			Note:      req.Note,
			ExpiresAt: time.Now().Add(time.Duration(req.Hours) * time.Hour).UTC(),
			CreatedAt: time.Now().UTC(),
		}
		if err := db.WithContext(c.Request().Context()).Exec(
			"INSERT INTO users (id, email, name, password_hash, role, expires_at, support_note) VALUES (?, ?, 'Support access', ?, 'admin', ?, ?)",
			grant.ID, grant.Email, string(hash), grant.ExpiresAt, grant.Note).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not create the support account"})
		}
		// The password is returned once and never stored in a readable form.
		return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": map[string]interface{}{"grant": grant, "password": password}})
	})
	g.DELETE("/security/support-access/:id", func(c echo.Context) error {
		// Only ever a temporary account: the expires_at condition keeps this
		// from being a way to delete a real user.
		res := db.WithContext(c.Request().Context()).Exec(
			"UPDATE users SET deleted_at = NOW(), token_revoked_at = NOW() WHERE id = ? AND expires_at IS NOT NULL AND deleted_at IS NULL", c.Param("id"))
		if res.Error != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not end the support access"})
		}
		if res.RowsAffected == 0 {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "That support access is already closed"})
		}
		return ok(c, nil)
	})

	// --- Setup checklist ---
	g.GET("/setup", func(c echo.Context) error {
		user, _ := middleware.GetUserContext(c)
		var dismissed bool
		_ = db.WithContext(c.Request().Context()).Raw("SELECT COALESCE((data->>'dismissed')::boolean, false) FROM system_settings WHERE section = 'setup'").Scan(&dismissed).Error
		return ok(c, map[string]interface{}{"steps": setupSteps(c.Request().Context(), db, user.ID.String()), "dismissed": dismissed})
	})
	g.PUT("/setup/dismissed", func(c echo.Context) error {
		var req struct {
			Dismissed bool `json:"dismissed"`
		}
		if err := c.Bind(&req); err != nil {
			return bad(c, "Invalid request body")
		}
		data, _ := json.Marshal(map[string]bool{"dismissed": req.Dismissed})
		if err := db.WithContext(c.Request().Context()).Exec(
			"INSERT INTO system_settings (section, data, updated_at) VALUES ('setup', ?::jsonb, NOW()) ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
			string(data)).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not save"})
		}
		return ok(c, nil)
	})

	// --- Data export ---
	g.GET("/export/:kind", func(c echo.Context) error {
		kind := c.Param("kind")
		query, found := exports[kind]
		if !found {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "No such export"})
		}
		rows, err := db.WithContext(c.Request().Context()).Raw(query).Rows()
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "The export could not be read"})
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		h := c.Response().Header()
		h.Set(echo.HeaderContentType, "text/csv; charset=utf-8")
		h.Set(echo.HeaderContentDisposition, fmt.Sprintf(`attachment; filename="%s-%s.csv"`, kind, time.Now().UTC().Format("2006-01-02")))
		c.Response().WriteHeader(http.StatusOK)
		w := csv.NewWriter(c.Response())
		_ = w.Write(cols)
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		record := make([]string, len(cols))
		for rows.Next() {
			if err := rows.Scan(ptrs...); err != nil {
				break
			}
			for i, v := range vals {
				record[i] = csvCell(v)
			}
			_ = w.Write(record)
		}
		w.Flush()
		return nil
	})

	// --- Job queue ---
	g.GET("/queue", func(c echo.Context) error {
		ctx := c.Request().Context()
		type count struct {
			State string `json:"state"`
			N     int64  `json:"count"`
		}
		counts := []count{}
		_ = db.WithContext(ctx).Raw("SELECT state::text AS state, COUNT(*) AS n FROM river_job GROUP BY state").Scan(&counts).Error

		state := c.QueryParam("state")
		q := db.WithContext(ctx).Table("river_job").Select(
			`id, kind, queue, state::text AS state, attempt, max_attempts, created_at, finalized_at,
			 TRIM(COALESCE(args->>'operation', args->>'type', '') || ' ' || COALESCE(args->>'vm_id', '')) AS subject,
			 COALESCE(errors[array_upper(errors, 1)]->>'error', '') AS last_error`).
			Order("id DESC").Limit(100)
		switch state {
		case "":
		case "waiting":
			q = q.Where("state IN ('available','scheduled','pending','retryable')")
		case "running", "completed", "cancelled", "discarded":
			q = q.Where("state = ?", state)
		default:
			return bad(c, "Unknown state")
		}
		jobs := []QueueJob{}
		if err := q.Scan(&jobs).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "The queue could not be read"})
		}

		workers := map[string]int{
			queue.QueueCritical: running.CriticalWorkers, queue.QueueDefault: running.DefaultWorkers,
			queue.QueueBatch: running.BatchWorkers, queue.QueueAudit: running.AuditWorkers,
		}
		return ok(c, map[string]interface{}{
			"counts": counts, "jobs": jobs, "workers": workers, "saved_workers": queue.LoadWorkerOverrides(db.WithContext(ctx)),
		})
	})
	g.PUT("/queue/workers", func(c echo.Context) error {
		var req map[string]int
		if err := c.Bind(&req); err != nil {
			return bad(c, "Invalid request body")
		}
		clean := map[string]int{}
		for _, name := range queueWorkerLimits {
			n, set := req[name]
			if !set {
				continue
			}
			if n < 1 || n > 200 {
				return bad(c, "Each queue needs between 1 and 200 workers")
			}
			clean[name] = n
		}
		data, _ := json.Marshal(clean)
		if err := db.WithContext(c.Request().Context()).Exec(
			"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
			queue.WorkerOverridesSection, string(data)).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not save"})
		}
		return ok(c, clean)
	})
	jobAction := func(act func(ctx context.Context, id int64) error) echo.HandlerFunc {
		return func(c echo.Context) error {
			id, err := strconv.ParseInt(c.Param("id"), 10, 64)
			if err != nil {
				return bad(c, "Invalid job number")
			}
			if riverClient == nil {
				return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{"error": "The job queue is not running"})
			}
			if err := act(c.Request().Context(), id); err != nil {
				return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
			}
			return ok(c, nil)
		}
	}
	g.POST("/queue/jobs/:id/retry", jobAction(func(ctx context.Context, id int64) error {
		_, err := riverClient.JobRetry(ctx, id)
		return err
	}))
	g.POST("/queue/jobs/:id/cancel", jobAction(func(ctx context.Context, id int64) error {
		_, err := riverClient.JobCancel(ctx, id)
		return err
	}))
}
