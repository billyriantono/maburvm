package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/riverqueue/river"
	"golang.org/x/crypto/bcrypt"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/handler"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/panel/vnc"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/maburvm/panel/internal/shared/queue"
	"gorm.io/gorm"
)

// Server represents the HTTP server
type Server struct {
	echo          *echo.Echo
	db            *gorm.DB
	cfg           *config.Config
	queueClient   *queue.Client
	riverClient   *river.Client[pgx.Tx]
	backupService *service.BackupService

	// readinessCheckTimeout bounds each dependency ping during /readyz.
	readinessCheckTimeout time.Duration

	// Dependency pings are overridable for testing without a live DB/queue. When
	// nil, production pings against the real DB and queue pool are used.
	dbPing    func(ctx context.Context) error
	queuePing func(ctx context.Context) error
}

// NewServer creates a new HTTP server instance
func NewServer(db *gorm.DB, cfg *config.Config) *Server {
	e := echo.New()

	// Pin agent TLS certificates: the panel records each node agent's self-signed
	// cert fingerprint on first connection and verifies it thereafter, so a
	// man-in-the-middle on the panel↔node network is rejected. Backed by the nodes
	// table; wired process-wide so every agent dial (pooled client + one-off
	// service dials) uses it.
	client.SetDefaultPinStore(newNodePinStore(repository.NewNodeRepository(db)))

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())

	// CORS with credentials. A configured ALLOWED_ORIGINS REPLACES the localhost
	// development fallback (it does not append). If unset, only the localhost
	// dev origin is allowed. Because NewServer cannot return an error without
	// broad churn, a wildcard ('*') origin — which is unsafe with credentials —
	// is rejected fail-closed. We use AllowOriginFunc so the decision is
	// explicit and the library cannot silently substitute '*' when the list is
	// empty; an origin is allowed only if it is on the resolved allow-list.
	allowedOrigins := parseAllowedOrigins(cfg.Server.AllowedOrigins)

	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOriginFunc: func(origin string) (bool, error) {
			for _, o := range allowedOrigins {
				if o == origin {
					return true, nil
				}
			}
			return false, nil
		},
		AllowMethods:     []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders:     []string{echo.HeaderOrigin, echo.HeaderContentType, echo.HeaderAccept, echo.HeaderAuthorization, echo.HeaderXRequestedWith},
		AllowCredentials: true,
		MaxAge:           86400,
	}))
	e.Use(middleware.Secure())
	e.Use(middleware.BodyLimit("10M"))
	e.Use(middleware.RequestID())

	return &Server{
		echo:                  e,
		db:                    db,
		cfg:                   cfg,
		readinessCheckTimeout: 5 * time.Second,
	}
}

// SetQueueClient wires the River queue into HTTP services that enqueue asynchronous work.
func (s *Server) SetQueueClient(client *queue.Client) {
	s.queueClient = client
	if client != nil {
		s.riverClient = client.RiverClient()
	}
}

// parseAllowedOrigins converts the ALLOWED_ORIGINS config value into a clean
// list of allowed CORS origins for use with credentials.
//
// Rules (fail-closed, Oracle requirement C):
//   - Empty/unset input → default to the localhost dev origin only. This keeps
//     local development working out of the box.
//   - A configured value REPLACES the localhost fallback (it does not append).
//   - Each entry is trimmed of surrounding spaces and a trailing slash.
//   - The wildcard '*' (or any entry containing it) is rejected: with
//     AllowCredentials enabled a wildcard would let any website drive
//     authenticated requests. Rejecting yields an empty slice, which makes the
//     CORS middleware deny every cross-origin request — observable and testable
//     rather than silently accepting everyone.
func parseAllowedOrigins(input string) []string {
	if strings.TrimSpace(input) == "" {
		return []string{"http://localhost:3000"}
	}
	out := make([]string, 0)
	for _, raw := range strings.Split(input, ",") {
		o := strings.TrimSpace(raw)
		o = strings.TrimSuffix(o, "/")
		if o == "" {
			continue
		}
		if o == "*" || strings.Contains(o, "*") {
			// Wildcard rejected: never allow with credentials.
			continue
		}
		out = append(out, o)
	}
	// If every entry was a rejected wildcard, out stays empty → CORS denies all.
	return out
}

func databaseURL(cfg config.DatabaseConfig) string {
	return cfg.DatabaseURL()
}

// SetupRoutes configures all API routes
func (s *Server) SetupRoutes() {
	// Health / liveness / readiness probes (process-only liveness + dependency
	// checks for readiness). Kept separate from DB-dependent routes so they can be
	// tested without a live database.
	s.setupHealthRoutes()

	// API v1 routes
	v1 := s.echo.Group("/api/v1")

	// Auth routes (no auth required)
	s.setupAuthRoutes(v1)

	// Node routes
	s.setupNodeRoutes(v1)

	// VM routes
	s.setupVMRoutes(v1)

	// Storage routes
	s.setupStorageRoutes(v1)

	// Template routes
	s.setupTemplateRoutes(v1)

	// Network routes
	s.setupNetworkRoutes()

	// IPAM / IP pool routes
	s.setupIPAMRoutes()

	// VPS plan (flavor) routes
	s.setupPlanRoutes()

	// API key routes (per-user automation credentials)
	s.setupAPIKeyRoutes()

	// SSH key routes (per-user public keys for VM provisioning)
	s.setupSSHKeyRoutes()

	// Recipe routes (per-user first-boot scripts)
	s.setupRecipeRoutes()

	// Public node bootstrap endpoints (install-agent.sh + prebuilt agent binary)
	s.setupProvisionRoutes()

	// Quota routes (per-user resource limits)
	s.setupQuotaRoutes()

	// Metrics history routes (persisted node monitoring)
	s.setupMetricsRoutes()

	// DNS routes (forward zones + records)
	s.setupDNSRoutes()

	// Snapshot routes
	s.setupSnapshotRoutes()

	// Backup routes
	s.setupBackupRoutes()

	// Import routes
	s.setupImportRoutes()

	// User management routes
	s.setupUserRoutes(v1)

	// Dashboard routes
	s.setupDashboardRoutes(v1)

	// Audit log routes
	s.setupAuditLogRoutes(v1)

	// Billing webhook routes (outside /api group, uses own auth)
	s.setupBillingRoutes()

	// Settings routes (profile, password, 2FA)
	s.setupSettingsRoutes(v1)

	// Notifications routes
	s.setupNotificationRoutes(v1)
}

// setupHealthRoutes registers liveness and readiness probes. Liveness is
// process-only (no dependency checks). Readiness pings the DB and queue (when
// initialized) with a bounded timeout and returns 503 + structured status when a
// dependency is nil/uninitialized/unreachable. Agents/nodes are never considered
// by readiness — their health is reported elsewhere.
func (s *Server) setupHealthRoutes() {
	// /health and /livez are aliases for the process liveness probe.
	s.echo.GET("/health", s.livenessHandler)
	s.echo.GET("/healthz", s.livenessHandler)
	s.echo.GET("/livez", s.livenessHandler)

	// /readyz performs dependency readiness checks.
	s.echo.GET("/readyz", s.readinessHandler)
}

// livenessHandler reports that the process is up. It intentionally performs NO
// dependency checks (DB/queue/agents), matching Kubernetes liveness semantics.
func (s *Server) livenessHandler(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

// readinessHandler reports dependency readiness. It checks the DB and the queue
// pool (when initialized) with a bounded timeout. Agents/nodes are deliberately
// NOT part of readiness: returning not-ready on their account would cause the
// panel to flap when a node is merely down.
func (s *Server) readinessHandler(c echo.Context) error {
	timeout := s.readinessCheckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(c.Request().Context(), timeout)
	defer cancel()

	deps := map[string]interface{}{}

	dbStatus := s.checkDB(ctx)
	deps["database"] = dbStatus

	queueStatus := s.checkQueue(ctx)
	deps["queue"] = queueStatus

	// Determine overall readiness.
	ready := true
	for _, d := range []map[string]interface{}{dbStatus, queueStatus} {
		if st, ok := d["status"].(string); ok && st != "ok" {
			ready = false
			break
		}
	}

	payload := map[string]interface{}{
		"status":       "ready",
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"dependencies": deps,
	}
	if !ready {
		payload["status"] = "not_ready"
		// 503 so orchestrators stop routing until dependencies recover.
		return c.JSON(http.StatusServiceUnavailable, payload)
	}
	return c.JSON(http.StatusOK, payload)
}

// checkDB pings the configured database within the request context. When no DB
// is wired (e.g. constructed for tests) it reports the dependency as missing
// rather than panicking.
func (s *Server) checkDB(ctx context.Context) map[string]interface{} {
	ping := s.dbPing
	if ping == nil {
		if s.db == nil {
			return map[string]interface{}{"status": "missing", "error": "database not initialized"}
		}
		ping = func(ctx context.Context) error {
			tx := s.db.WithContext(ctx).Raw("SELECT 1")
			if err := tx.Error; err != nil {
				return err
			}
			if err := tx.Exec("SELECT 1").Error; err != nil {
				return err
			}
			return nil
		}
	}
	if err := ping(ctx); err != nil {
		return map[string]interface{}{"status": "error", "error": err.Error()}
	}
	return map[string]interface{}{"status": "ok"}
}

// checkQueue pings the River queue pool (when the client is initialized). When
// the queue is not wired it is treated as missing (not ready) rather than
// panicking.
func (s *Server) checkQueue(ctx context.Context) map[string]interface{} {
	ping := s.queuePing
	if ping == nil {
		if s.queueClient == nil {
			return map[string]interface{}{"status": "missing", "error": "queue not initialized"}
		}
		pool := s.queueClient.Pool()
		if pool == nil {
			return map[string]interface{}{"status": "missing", "error": "queue pool not initialized"}
		}
		ping = func(ctx context.Context) error {
			return pool.Ping(ctx)
		}
	}
	if err := ping(ctx); err != nil {
		return map[string]interface{}{"status": "error", "error": err.Error()}
	}
	return map[string]interface{}{"status": "ok"}
}

// setupAuthRoutes configures authentication-related routes
func (s *Server) setupAuthRoutes(g *echo.Group) {
	userService, err := service.NewUserService(
		s.db,
		s.cfg.JWT.AESKey,
		s.cfg.JWT.SecretKey,
		"maburvm-panel",
	)
	if err != nil {
		slog.Default().Error("failed to create user service", "error", err)
		return
	}

	authHandler := handler.NewAuthHandler(userService)

	auth := g.Group("/auth")
	auth.POST("/login", authHandler.Login, panelMiddleware.LoginRateLimiter())
	auth.POST("/register", authHandler.Register)
	auth.POST("/forgot-password", authHandler.ForgotPassword, panelMiddleware.LoginRateLimiter())
	auth.POST("/reset-password", authHandler.ResetPassword, panelMiddleware.LoginRateLimiter())
	auth.POST("/logout", authHandler.Logout, panelMiddleware.RequireAuth(s.db))
	auth.GET("/me", authHandler.Me, panelMiddleware.RequireAuth(s.db))
	auth.GET("/client-ip", authHandler.ClientIP)
}

// setupNodeRoutes configures node-related routes
func (s *Server) setupNodeRoutes(g *echo.Group) {
	// Initialize repository
	nodeRepo := repository.NewNodeRepository(s.db)

	// Initialize service
	nodeService := service.NewNodeService(nodeRepo, s.db)

	// Initialize handler
	nodeHandler := handler.NewNodeHandler(nodeService)

	// Create node routes group
	nodes := g.Group("/nodes")

	// Apply authentication middleware to all node routes
	nodes.Use(panelMiddleware.RequireAuth(s.db))

	// Routes
	// List nodes - requires node:read permission
	nodes.GET("", nodeHandler.ListNodes, panelMiddleware.RequirePermission("node:read"))

	// Get node details - requires node:read permission
	nodes.GET("/:id", nodeHandler.GetNode, panelMiddleware.RequirePermission("node:read"))

	// Register new node - requires node:create permission
	nodes.POST("", nodeHandler.RegisterNode, panelMiddleware.RequirePermission("node:create"))

	// Update node - requires node:update permission
	nodes.PUT("/:id", nodeHandler.UpdateNode, panelMiddleware.RequirePermission("node:update"))

	// Delete node - requires node:delete permission
	nodes.DELETE("/:id", nodeHandler.DeleteNode, panelMiddleware.RequirePermission("node:delete"))

	// Regenerate token - requires node:update permission
	nodes.POST("/:id/regenerate-token", nodeHandler.RegenerateToken, panelMiddleware.RequirePermission("node:update"))

	// Get node token - requires node:update permission
	nodes.GET("/:id/token", nodeHandler.GetNodeToken, panelMiddleware.RequirePermission("node:update"))

	// Get node metrics - requires node:read permission
	nodes.GET("/:id/metrics", nodeHandler.GetNodeMetrics, panelMiddleware.RequirePermission("node:read"))
}

// setupVMRoutes configures VM-related routes
func (s *Server) setupVMRoutes(g *echo.Group) {
	logger := slog.Default()

	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)

	vmService := service.NewVMService(s.db, vmRepo, nodeRepo, templateRepo, s.riverClient, logger)

	wsHost := ""
	if s.cfg.Server.Host != "" {
		wsHost = fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	}

	vncService, err := service.NewVNCService(s.db, vmRepo, nodeRepo, logger, s.cfg.JWT.SecretKey, wsHost)
	if err != nil {
		logger.Error("failed to initialize VNC service", "error", err)
		return
	}

	if err := vncService.Migrate(); err != nil {
		logger.Error("failed to migrate console tokens table", "error", err)
	}

	// Create VNC proxy server for WebSocket proxying
	vncProxyServer := vnc.NewProxyServer(s.cfg, nil, logger, s.cfg.JWT.SecretKey)

	// Register VNC WebSocket endpoint
	vncHandler := vnc.NewHandler(vncProxyServer)
	s.echo.GET("/ws/vnc", vncHandler.HandleWebSocket)

	// In-browser SSH console (xterm.js ↔ SSH shell), sibling of the VNC console.
	s.setupSSHConsoleRoutes(logger)

	vmHandler := handler.NewVMHandler(vmService, vncService, vncProxyServer, service.NewSSHKeyService(s.db), service.NewRecipeService(s.db), authz.NewAuthorizer(s.db))

	// Images: user-owned standalone disk captures that survive VM deletion and can
	// seed a new VM (create-from-image). Shares the agent BackupDisk export.
	imageService := service.NewImageService(s.db, s.riverClient, logger)
	vmHandler.SetImageService(imageService)
	handler.RegisterImageRoutes(s.echo, handler.NewImageHandler(imageService), s.db)

	handler.RegisterVMRoutes(s.echo, vmHandler, s.db)

	// Floating (elastic) IPs: host-side 1:1-NATed addresses that move between VMs
	// on a node and outlive the VM they were attached to. Shares VMService for its
	// agent connections and IPAM access.
	handler.RegisterFloatingIPRoutes(s.echo, handler.NewFloatingIPHandler(vmService), s.db)

	// Regions: the location a customer picks when ordering.
	regionService := service.NewRegionService(s.db)
	handler.RegisterRegionRoutes(s.echo, handler.NewRegionHandler(regionService), s.db)
	vmService.SetRegionService(regionService)

	// Abuse visibility: which guests are opening new outbound connections fast
	// enough to threaten the node they sit on, including guests the panel does
	// not manage. Shares the node service's agent connection pool.
	abuseService := service.NewAbuseService(s.db, service.NewNodeService(repository.NewNodeRepository(s.db), s.db).AgentClient(), nil)
	handler.RegisterAbuseRoutes(s.echo, handler.NewAbuseHandler(abuseService, repository.NewAuditRepository(s.db)), s.db)

	// Tenant VPCs: customer-defined private networks. Two customers may choose the
	// same subnet — a router namespace per VPC on the node keeps them apart.
	vpcService := service.NewVPCService(s.db, service.NewIPAMService(s.db, repository.NewIPAMRepository(s.db)))
	handler.RegisterVPCRoutes(s.echo, handler.NewVPCHandler(vpcService), s.db)
	vmService.SetVPCService(vpcService)

	// Real-time VM status stream (SSE), consumed by the dashboard via the web BFF.
	eventsHandler := handler.NewEventsHandler(vmService)
	events := s.echo.Group("/api/v1/events")
	events.Use(panelMiddleware.RequireAuth(s.db))
	events.GET("/vm-status", eventsHandler.StreamVMStatus, panelMiddleware.RequirePermission("vm:read"))

	// Bandwidth usage routes (nested under VMs)
	bwRepo := repository.NewBandwidthUsageRepository(s.db)
	bwService := service.NewBandwidthService(bwRepo, logger)
	bwHandler := handler.NewBandwidthHandler(bwService, authz.NewAuthorizer(s.db))

	handler.RegisterBandwidthRoutes(s.echo, bwHandler, s.db)
}

// setupStorageRoutes configures storage-related routes
func (s *Server) setupStorageRoutes(g *echo.Group) {
	storageRepo := repository.NewStorageRepository(s.db)
	// Provision real volumes on node agents (qcow2/raw via qemu-img), not just metadata.
	storageService := service.NewStorageServiceWithProvisioner(
		storageRepo,
		repository.NewNodeRepository(s.db),
		service.NewAgentVolumeProvisioner(0),
	)
	storageHandler := handler.NewStorageHandler(storageService, authz.NewAuthorizer(s.db))

	storage := g.Group("/storage")
	storage.Use(panelMiddleware.RequireAuth(s.db))

	storage.GET("/pools", storageHandler.GetPools)
	storage.GET("/pools/:id", storageHandler.GetPoolByID)
	storage.POST("/pools", storageHandler.CreatePool, panelMiddleware.RequirePermission("storage:create"))
	storage.PUT("/pools/:id", storageHandler.UpdatePool, panelMiddleware.RequirePermission("storage:update"))
	storage.DELETE("/pools/:id", storageHandler.DeletePool, panelMiddleware.RequirePermission("storage:delete"))

	storage.GET("/pools/:poolId/volumes", storageHandler.GetVolumes)
	storage.POST("/pools/:poolId/volumes", storageHandler.CreateVolume, panelMiddleware.RequirePermission("storage:create"))
	storage.DELETE("/pools/:poolId/volumes/:volumeId", storageHandler.DeleteVolume, panelMiddleware.RequirePermission("storage:delete"))
}

// setupTemplateRoutes configures template-related routes
func (s *Server) setupTemplateRoutes(g *echo.Group) {
	templateRepo := repository.NewTemplateRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateService := service.NewTemplateService(s.db, templateRepo, nodeRepo, s.riverClient)
	templateHandler := handler.NewTemplateHandler(templateService)
	templateHandler.RegisterRoutes(s.echo, panelMiddleware.RequireAuth(s.db))
}

// setupNetworkRoutes configures network-related routes
func (s *Server) setupNetworkRoutes() {
	networkRepo := repository.NewNetworkRepository(s.db)
	firewallRepo := repository.NewFirewallRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	networkService := service.NewNetworkService(s.db, networkRepo, firewallRepo, vmRepo, nodeRepo, s.riverClient)
	networkHandler := handler.NewNetworkHandler(networkService, authz.NewAuthorizer(s.db))
	handler.RegisterNetworkRoutes(s.echo, networkHandler, s.db)

	// Standalone /api/v1/networks endpoint: administrator-defined virtual networks
	// (bridge/NAT/isolated) — persisted, not phantom. Per-VM IPs live under IP Pools.
	managedNetRepo := repository.NewManagedNetworkRepository(s.db)
	mnService := service.NewManagedNetworkService(s.db)
	networks := s.echo.Group("/api/v1/networks")
	networks.Use(panelMiddleware.RequireAuth(s.db))
	networks.GET("", s.handleListManagedNetworks(managedNetRepo))
	networks.POST("", func(c echo.Context) error {
		var net models.ManagedNetwork
		if err := c.Bind(&net); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request"})
		}
		if net.Name == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Network name is required"})
		}
		if net.Type == "" {
			net.Type = "bridge"
		}
		// Create persists the record and, for isolated/NAT types on a node,
		// provisions the real libvirt network there (recording its bridge).
		if err := mnService.Create(c.Request().Context(), &net); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": net})
	}, panelMiddleware.RequirePermission("network:create"))
	networks.DELETE("/:id", func(c echo.Context) error {
		if err := mnService.Delete(c.Request().Context(), c.Param("id")); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Network deleted"})
	}, panelMiddleware.RequirePermission("network:delete"))
}

// setupIPAMRoutes configures first-class IPAM / IP pool routes.
func (s *Server) setupIPAMRoutes() {
	ipamRepo := repository.NewIPAMRepository(s.db)
	ipamService := service.NewIPAMService(s.db, ipamRepo)
	// Live PTR push (PowerDNS) when configured — rDNS works without a forward zone.
	ipamService.SetDNSProvider(service.NewDNSProviderFromEnv())
	ipamHandler := handler.NewIPAMHandler(ipamService)
	handler.RegisterIPAMRoutes(s.echo, ipamHandler, s.db)
}

// setupPlanRoutes configures VPS plan (flavor) routes.
func (s *Server) setupPlanRoutes() {
	planRepo := repository.NewPlanRepository(s.db)
	planService := service.NewPlanService(planRepo)
	planHandler := handler.NewPlanHandler(planService)
	handler.RegisterPlanRoutes(s.echo, planHandler, s.db)
}

// setupAPIKeyRoutes configures per-user API key routes for automation.
func (s *Server) setupAPIKeyRoutes() {
	apiKeyService := service.NewAPIKeyService(s.db)
	apiKeyHandler := handler.NewAPIKeyHandler(apiKeyService)
	handler.RegisterAPIKeyRoutes(s.echo, apiKeyHandler, s.db)
}

// setupSSHKeyRoutes configures per-user SSH public key routes for VM provisioning.
func (s *Server) setupSSHKeyRoutes() {
	sshKeyService := service.NewSSHKeyService(s.db)
	sshKeyHandler := handler.NewSSHKeyHandler(sshKeyService)
	handler.RegisterSSHKeyRoutes(s.echo, sshKeyHandler, s.db)
}

// setupRecipeRoutes configures per-user first-boot recipe routes (saved scripts
// applied to a VM on first boot via cloud-init).
func (s *Server) setupRecipeRoutes() {
	recipeService := service.NewRecipeService(s.db)
	recipeHandler := handler.NewRecipeHandler(recipeService)
	handler.RegisterRecipeRoutes(s.echo, recipeHandler, s.db)
}

// setupProvisionRoutes serves the node bootstrap installer + prebuilt agent
// binary so a new node can be enrolled with a single copy-paste command.
func (s *Server) setupProvisionRoutes() {
	binaryDir := os.Getenv("AGENT_BINARY_DIR")
	publicURL := os.Getenv("PANEL_PUBLIC_URL")
	handler.RegisterProvisionRoutes(s.echo, handler.NewProvisionHandler(binaryDir, publicURL))
}

// setupQuotaRoutes configures per-user resource quota routes.
func (s *Server) setupQuotaRoutes() {
	quotaService := service.NewQuotaService(s.db, repository.NewVMRepository(s.db))
	quotaHandler := handler.NewQuotaHandler(quotaService)
	handler.RegisterQuotaRoutes(s.echo, quotaHandler, s.db)
}

// setupMetricsRoutes configures persisted metric history routes.
func (s *Server) setupMetricsRoutes() {
	metricsService := service.NewMetricsService(s.db)
	metricsHandler := handler.NewMetricsHandler(metricsService, authz.NewAuthorizer(s.db))
	handler.RegisterMetricsRoutes(s.echo, metricsHandler, s.db)
}

// setupDNSRoutes configures forward DNS zone/record routes. A live nameserver
// provider (PowerDNS) is wired from env when configured; otherwise export-only.
func (s *Server) setupDNSRoutes() {
	dnsService := service.NewDNSServiceWithProvider(s.db, service.NewDNSProviderFromEnv(), slog.Default())
	dnsHandler := handler.NewDNSHandler(dnsService)
	handler.RegisterDNSRoutes(s.echo, dnsHandler, s.db)
}

// setupSnapshotRoutes configures snapshot-related routes
func (s *Server) setupSnapshotRoutes() {
	logger := slog.Default()
	snapshotRepo := repository.NewSnapshotRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	snapshotService := service.NewSnapshotService(s.db, snapshotRepo, vmRepo, nodeRepo, s.riverClient, logger)
	snapshotHandler := handler.NewSnapshotHandler(snapshotService, authz.NewAuthorizer(s.db))
	handler.RegisterSnapshotRoutes(s.echo, snapshotHandler, s.db)
}

// setupBackupRoutes configures backup-related routes
func (s *Server) setupBackupRoutes() {
	logger := slog.Default()
	backupRepo := repository.NewBackupRepository(s.db)
	scheduleRepo := repository.NewBackupScheduleRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	backupService := service.NewBackupService(s.db, backupRepo, scheduleRepo, vmRepo, nodeRepo, s.riverClient, nil, logger)
	// Keep a reference so the scheduler (cron) is started at boot and stopped on
	// shutdown — without Start() scheduled backups never fire.
	s.backupService = backupService
	backupHandler := handler.NewBackupHandler(backupService)
	handler.RegisterBackupRoutes(s.echo, backupHandler, s.db)
}

// setupImportRoutes configures VM import routes
func (s *Server) setupImportRoutes() {
	logger := slog.Default()
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)
	networkRepo := repository.NewNetworkRepository(s.db)
	importService := service.NewImportService(s.db, vmRepo, nodeRepo, templateRepo, networkRepo, s.riverClient, logger)
	importHandler := handler.NewImportHandler(importService)
	handler.RegisterImportRoutes(s.echo, importHandler, s.db)
}

// setupUserRoutes configures user management routes (CRUD beyond auth)
func (s *Server) setupUserRoutes(g *echo.Group) {
	userRepo := repository.NewUserRepository(s.db)

	users := g.Group("/users")
	users.Use(panelMiddleware.RequireAuth(s.db))
	users.Use(panelMiddleware.RequirePermission("user:read"))

	users.GET("", func(c echo.Context) error {
		limit := 100
		offset := 0
		userList, err := userRepo.List(c.Request().Context(), limit, offset)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list users"})
		}
		total, _ := userRepo.Count(c.Request().Context())
		return c.JSON(http.StatusOK, map[string]interface{}{
			"data":       userList,
			"total":      total,
			"page":       1,
			"page_size":  limit,
			"totalPages": (int(total) + limit - 1) / limit,
		})
	})

	users.GET("/:id", func(c echo.Context) error {
		id, err := parseUUID(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid user ID"})
		}
		user, err := userRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "User not found"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"data": user})
	})

	users.POST("", func(c echo.Context) error {
		var req struct {
			Name             string          `json:"name"`
			Email            string          `json:"email"`
			Password         string          `json:"password"`
			Role             models.UserRole `json:"role"`
			IPWhitelist      []string        `json:"ip_whitelist"`
			SendWelcomeEmail bool            `json:"send_welcome_email"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}
		if req.Email == "" || req.Password == "" {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Email and password are required"})
		}
		if req.Role == "" {
			req.Role = models.RoleClient
		}
		if req.Role != models.RoleAdmin && req.Role != models.RoleClient {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid role"})
		}
		if err := models.ValidateIPWhitelist(req.IPWhitelist); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}

		ctx := c.Request().Context()

		// Admin-initiated direct creation of a CLIENT account. The public
		// invite-only self-enrollment flow (Phase 1B) is a separate path; an
		// authenticated admin holding user:create may provision a client directly
		// from the panel's "Add New User" page.
		if req.Role == models.RoleClient {
			adminCaller, ok := panelMiddleware.GetUserContext(c)
			if !ok || adminCaller.Role != models.RoleAdmin {
				return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Insufficient permissions"})
			}
			repo := repository.NewUserRepository(s.db)
			if exists, err := repo.EmailExists(ctx, req.Email); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "email check failed"})
			} else if exists {
				return c.JSON(http.StatusConflict, map[string]interface{}{"error": "email_exists"})
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
			if err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "hash failed"})
			}
			client := models.User{
				Name:         req.Name,
				Email:        req.Email,
				PasswordHash: string(hash),
				Role:         models.RoleClient,
				IPWhitelist:  req.IPWhitelist,
			}
			if err := repo.Create(ctx, &client); err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "failed to create user"})
			}
			// Best-effort welcome email (never fails the creation).
			if req.SendWelcomeEmail {
				if cfg, ok, serr := service.LoadSMTPSettings(s.db); serr == nil && ok {
					if werr := service.SendWelcomeEmail(cfg, client.Email, client.Name, os.Getenv("PANEL_PUBLIC_URL")); werr != nil {
						slog.Default().Warn("welcome email failed", "email", client.Email, "error", werr)
					}
				} else {
					slog.Default().Warn("welcome email skipped: SMTP not configured", "email", client.Email)
				}
			}
			return c.JSON(http.StatusCreated, map[string]interface{}{"data": client})
		}

		// Creating an admin is an interim exception restricted to the founding
		// administrator. Peers (ordinary admins) cannot mint new admins. The
		// comparison is against the earliest active admin, resolved from the
		// users table, which is deterministic and avoids trust in frontend state.
		caller, ok := panelMiddleware.GetUserContext(c)
		if !ok || caller.Role != models.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Insufficient permissions"})
		}

		var created models.User
		rbErr := s.execRoleBoundaryTx(ctx, func(tx *gorm.DB) error {
			// Recheck inside the locked transaction: the caller must still be the
			// active founding admin at the moment of mutation.
			if authErr := recheckFoundingAuthority(tx, ctx, caller.ID, "only_the_founding_administrator_may_create_admins"); authErr != nil {
				return authErr
			}
			repo := repository.NewUserRepository(tx)
			if exists, err := repo.EmailExists(ctx, req.Email); err != nil {
				return fmt.Errorf("email check: %w", err)
			} else if exists {
				return &roleBoundaryErr{status: http.StatusConflict, code: "email_exists"}
			}
			hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
			if err != nil {
				return fmt.Errorf("hash: %w", err)
			}
			created = models.User{
				Name:         req.Name,
				Email:        req.Email,
				PasswordHash: string(hash),
				Role:         models.RoleAdmin,
				IPWhitelist:  req.IPWhitelist,
			}
			if err := repo.Create(ctx, &created); err != nil {
				return fmt.Errorf("create: %w", err)
			}
			return nil
		})
		if rbErr != nil {
			return writeRoleBoundaryErr(c, rbErr)
		}
		return c.JSON(http.StatusCreated, map[string]interface{}{"data": created})
	}, panelMiddleware.RequirePermission("user:create"))

	users.PUT("/:id", func(c echo.Context) error {
		id, err := parseUUID(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid user ID"})
		}
		user, err := userRepo.GetByID(c.Request().Context(), id)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "User not found"})
		}
		ctx := c.Request().Context()
		var req struct {
			Email       string   `json:"email"`
			Role        string   `json:"role"`
			IPWhitelist []string `json:"ip_whitelist"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}
		// Validate any supplied IP whitelist (reject malformed input early). This
		// only affects a non-security field; it never touches role/quota_mode.
		if req.IPWhitelist != nil {
			if err := models.ValidateIPWhitelist(req.IPWhitelist); err != nil {
				return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
			}
		}

		// Determine whether this request crosses the admin role boundary: it does
		// when a role is supplied that differs from the caller-fetched current role.
		isRoleTransition := req.Role != "" && models.UserRole(req.Role) != user.Role

		// Non-role update (e.g. email / ip_whitelist). This is a permitted operation
		// that does not require founding authority and is NOT serialized by the
		// advisory lock. CRITICAL: we must not use a full-model Save of the pre-read
		// User, because that would write back a STALE `role` and clobber a concurrent
		// serialized role transition. Instead we apply a SELECTIVE UPDATE restricted
		// to an explicit allow-list of non-role columns (email, ip_whitelist);
		// "role" (and every security/quota field) is dropped on purpose by
		// updateUserNonRoleFields.
		if !isRoleTransition {
			fields := map[string]interface{}{}
			if req.Email != "" {
				fields["email"] = req.Email
			}
			if req.IPWhitelist != nil {
				fields["ip_whitelist"] = req.IPWhitelist
			}
			if len(fields) > 0 {
				if err := updateUserNonRoleFields(s.db, ctx, id, fields); err != nil {
					return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to update user"})
				}
			}
			// Re-read to return the authoritative, current row (reflecting any
			// concurrent committed role change rather than the stale pre-read).
			updated, gerr := userRepo.GetByID(ctx, id)
			if gerr != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to load user"})
			}
			return c.JSON(http.StatusOK, map[string]interface{}{"data": updated})
		}

		// Role transitions are gated. Any request that would change the role —
		// promotion (to admin) or demotion (away from admin) — is an admin
		// privilege transition and is restricted to the founding administrator.
		if req.Role != string(models.RoleAdmin) && req.Role != string(models.RoleClient) {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid role"})
		}

		newRole := models.UserRole(req.Role)
		caller, ok := panelMiddleware.GetUserContext(c)
		if !ok || caller.Role != models.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Insufficient permissions"})
		}

		// Serialize the entire role transition (recheck + last-admin count +
		// mutation) in one transaction behind the role-boundary advisory lock so
		// concurrent demote/delete cannot leave zero active admins.
		rbErr := s.execRoleBoundaryTx(ctx, func(tx *gorm.DB) error {
			if authErr := recheckFoundingAuthority(tx, ctx, caller.ID, "only_the_founding_administrator_may_change_admin_roles"); authErr != nil {
				return authErr
			}
			// Re-read the target within the transaction; it may have changed since
			// the HTTP-layer fetch.
			repo := repository.NewUserRepository(tx)
			target, gerr := repo.GetByID(ctx, id)
			if gerr != nil {
				return &roleBoundaryErr{status: http.StatusNotFound, code: "user_not_found"}
			}
			// Last-admin guard: a demotion that would remove the final active
			// administrator is forbidden. Count active admins EXCLUDING the target,
			// so if the target is the only remaining active admin and we are about
			// to demote it, the count is zero and we reject.
			if target.Role == models.RoleAdmin && newRole != models.RoleAdmin {
				remaining, cerr := countActiveAdminsExceptTx(tx, ctx, target.ID)
				if cerr != nil {
					return fmt.Errorf("admin count: %w", cerr)
				}
				if remaining <= 0 {
					return &roleBoundaryErr{status: http.StatusForbidden, code: "cannot_demote_last_active_admin"}
				}
			}
			if req.Email != "" {
				target.Email = req.Email
			}
			// A role transition may be combined with an allowed non-role field
			// (ip_whitelist). The target row is freshly read INSIDE this locked
			// transaction, so quota_mode/role changes are consistent and any
			// concurrent transition is serialized by pg_advisory_xact_lock.
			if req.IPWhitelist != nil {
				target.IPWhitelist = req.IPWhitelist
			}
			target.Role = newRole
			if err := repo.Update(ctx, target); err != nil {
				return fmt.Errorf("update: %w", err)
			}
			return nil
		})
		if rbErr != nil {
			return writeRoleBoundaryErr(c, rbErr)
		}
		// Reflect the mutation back into the outer user object for the response.
		updated, gerr := userRepo.GetByID(ctx, id)
		if gerr != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to load user"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"data": updated})
	}, panelMiddleware.RequirePermission("user:update"))

	users.DELETE("/:id", func(c echo.Context) error {
		id, err := parseUUID(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid user ID"})
		}
		ctx := c.Request().Context()

		caller, ok := panelMiddleware.GetUserContext(c)
		if !ok || caller.Role != models.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Insufficient permissions"})
		}

		rbErr := s.execRoleBoundaryTx(ctx, func(tx *gorm.DB) error {
			// Founding-admin authority enforcement: only the earliest active
			// founding admin may delete an admin. A peer admin cannot delete the
			// founder or any other admin and seize authority.
			if authErr := recheckFoundingAuthority(tx, ctx, caller.ID, "only_the_founding_administrator_may_delete_admins"); authErr != nil {
				return authErr
			}
			repo := repository.NewUserRepository(tx)
			target, gerr := repo.GetByID(ctx, id)
			if gerr != nil {
				return &roleBoundaryErr{status: http.StatusNotFound, code: "user_not_found"}
			}
			// Last-admin guard: a delete that would remove the only remaining
			// active administrator is forbidden so the control plane can never be
			// left with zero admins.
			if target.Role == models.RoleAdmin {
				remaining, cerr := countActiveAdminsExceptTx(tx, ctx, target.ID)
				if cerr != nil {
					return fmt.Errorf("admin count: %w", cerr)
				}
				if remaining <= 0 {
					return &roleBoundaryErr{status: http.StatusForbidden, code: "cannot_delete_last_active_admin"}
				}
			}
			if err := repo.Delete(ctx, id); err != nil {
				return fmt.Errorf("delete: %w", err)
			}
			return nil
		})
		if rbErr != nil {
			return writeRoleBoundaryErr(c, rbErr)
		}
		return c.NoContent(http.StatusNoContent)
	}, panelMiddleware.RequirePermission("user:delete"))
}

// setupDashboardRoutes configures dashboard statistics routes
func (s *Server) setupDashboardRoutes(g *echo.Group) {
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	auditRepo := repository.NewAuditRepository(s.db)

	dashboard := g.Group("/dashboard")
	dashboard.Use(panelMiddleware.RequireAuth(s.db))
	// Admin-only: these stats are fleet-wide (all nodes/VMs) and include recent
	// audit-log entries across all tenants. Clients use their own /client
	// dashboard, which is scoped to their VMs.
	dashboard.Use(panelMiddleware.RequirePermission("admin:access"))

	dashboard.GET("/stats", func(c echo.Context) error {
		ctx := c.Request().Context()

		// VM counts
		totalVMs, _ := vmRepo.Count(ctx)
		runningVMs, _ := vmRepo.CountByStatus(ctx, models.VMStatusRunning)
		stoppedVMs, _ := vmRepo.CountByStatus(ctx, models.VMStatusStopped)
		errorVMs, _ := vmRepo.CountByStatus(ctx, models.VMStatusError)

		// Node counts
		totalNodes, _ := nodeRepo.Count(ctx)
		activeNodes, _ := nodeRepo.CountByStatus(ctx, models.NodeStatusActive)

		// Recent activity (last 10 audit logs), enriched for display with the
		// actor's email and a human-readable resource name so the feed doesn't
		// show bare UUIDs / "user".
		recentLogs, _ := auditRepo.List(ctx, 10, 0)
		recentActivity := enrichActivity(s.db, recentLogs)

		// Calculate utilization
		var utilization float64
		if totalVMs > 0 {
			utilization = float64(runningVMs) / float64(totalVMs) * 100
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"data": map[string]interface{}{
				"vms": map[string]interface{}{
					"total":   totalVMs,
					"running": runningVMs,
					"stopped": stoppedVMs,
					"error":   errorVMs,
				},
				"nodes": map[string]interface{}{
					"total":  totalNodes,
					"active": activeNodes,
				},
				"utilization":     utilization,
				"alerts":          errorVMs,
				"recent_activity": recentActivity,
			},
		})
	})
}

// activityEntry is a display-ready recent-activity row: the raw audit action
// plus a resolved actor email and a human-readable resource name.
type activityEntry struct {
	ID           string    `json:"id"`
	Action       string    `json:"action"`
	Actor        string    `json:"actor"` // user email, or "System"
	ResourceType string    `json:"resource_type"`
	ResourceName string    `json:"resource_name"` // e.g. VM hostname, or "" when unknown
	ResourceID   string    `json:"resource_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// enrichActivity turns raw audit logs into display-ready entries: it batch-loads
// the actor emails and derives a resource name from the log's details (hostname)
// so the dashboard feed shows "root@… • Created VM web-01" instead of a UUID.
func enrichActivity(db *gorm.DB, logs []models.AuditLog) []activityEntry {
	// Batch-resolve actor emails.
	ids := make([]string, 0, len(logs))
	seen := map[string]bool{}
	for _, l := range logs {
		if l.UserID != nil && !seen[*l.UserID] {
			seen[*l.UserID] = true
			ids = append(ids, *l.UserID)
		}
	}
	emails := map[string]string{}
	if len(ids) > 0 {
		var rows []struct {
			ID    string
			Email string
		}
		_ = db.Model(&models.User{}).Select("id", "email").Where("id IN ?", ids).Scan(&rows).Error
		for _, r := range rows {
			emails[r.ID] = r.Email
		}
	}

	out := make([]activityEntry, 0, len(logs))
	for _, l := range logs {
		actor := "System"
		if l.UserID != nil {
			if e, ok := emails[*l.UserID]; ok && e != "" {
				actor = e
			} else {
				actor = "user"
			}
		}
		name := ""
		if l.Details != nil {
			if h, ok := l.Details["hostname"].(string); ok {
				name = h
			}
		}
		resourceID := ""
		if l.ResourceID != nil {
			resourceID = *l.ResourceID
		}
		out = append(out, activityEntry{
			ID:           l.ID,
			Action:       l.Action,
			Actor:        actor,
			ResourceType: l.ResourceType,
			ResourceName: name,
			ResourceID:   resourceID,
			CreatedAt:    l.CreatedAt,
		})
	}
	return out
}

// setupAuditLogRoutes configures audit log viewing routes
func (s *Server) setupAuditLogRoutes(g *echo.Group) {
	auditRepo := repository.NewAuditRepository(s.db)

	auditLogs := g.Group("/audit-logs")
	auditLogs.Use(panelMiddleware.RequireAuth(s.db))
	auditLogs.Use(panelMiddleware.RequirePermission("audit:read"))

	auditLogs.GET("", func(c echo.Context) error {
		limit := 20
		page := 1
		if l := c.QueryParam("page_size"); l != "" {
			if parsed, err := fmt.Sscanf(l, "%d", &limit); err == nil && parsed > 0 && limit > 0 {
				if limit > 100 {
					limit = 100
				}
			}
		}
		if p := c.QueryParam("page"); p != "" {
			if parsed, err := fmt.Sscanf(p, "%d", &page); err == nil && parsed > 0 && page > 0 {
				// valid
			}
		}
		offset := (page - 1) * limit

		action := c.QueryParam("action")
		userID := c.QueryParam("user_id")

		var logs []models.AuditLog
		var total int64
		var err error

		if action != "" {
			logs, err = auditRepo.ListByAction(c.Request().Context(), action, limit, offset)
			total, _ = auditRepo.CountByAction(c.Request().Context(), action)
		} else if userID != "" {
			logs, err = auditRepo.ListByUser(c.Request().Context(), userID, limit, offset)
			total, _ = auditRepo.CountByUser(c.Request().Context(), userID)
		} else {
			logs, err = auditRepo.List(c.Request().Context(), limit, offset)
			total, _ = auditRepo.Count(c.Request().Context())
		}

		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list audit logs"})
		}

		totalPages := (int(total) + limit - 1) / limit
		if totalPages < 1 {
			totalPages = 1
		}

		return c.JSON(http.StatusOK, map[string]interface{}{
			"data":       logs,
			"total":      total,
			"page":       page,
			"page_size":  limit,
			"totalPages": totalPages,
		})
	})
}

// parseUUID parses a UUID string
func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// foundingAdminID returns the ID of the earliest-created active administrator —
// the founding administrator. This is the single, deterministic source of truth
// for the interim admin exception in this lane: only the founding admin may
// create, promote, demote, or delete administrators, and the founding admin is
// resolved by creation order (not by an ad-hoc flag), which is reliably
// reproducible from the existing users table (created_at ASC) without a dedicated
// superadmin role. Returns uuid.Nil when no active admin exists. Delegates to the
// transaction-scoped resolver so behavior matches the locked role boundary.
func (s *Server) foundingAdminID(ctx context.Context) uuid.UUID {
	return foundingAdminIDTx(s.db, ctx)
}

// setupBillingRoutes configures billing webhook routes with HMAC authentication
func (s *Server) setupBillingRoutes() {
	logger := slog.Default()

	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)

	vmService := service.NewVMService(s.db, vmRepo, nodeRepo, templateRepo, s.riverClient, logger)

	billingHandler := handler.NewBillingHandler(vmService, logger, s.db)
	handler.RegisterBillingRoutes(s.echo, billingHandler)
}

// setupSettingsRoutes configures user settings routes (profile, password, 2FA)
func (s *Server) setupSettingsRoutes(g *echo.Group) {
	userRepo := repository.NewUserRepository(s.db)
	// Real TOTP-backed 2FA service (encrypted secrets, QR, backup codes).
	userService, userSvcErr := service.NewUserService(s.db, s.cfg.JWT.AESKey, s.cfg.JWT.SecretKey, "maburvm-panel")
	if userSvcErr != nil {
		slog.Default().Error("settings: failed to init user service for 2FA", "error", userSvcErr)
	}

	settings := g.Group("/settings")
	settings.Use(panelMiddleware.RequireAuth(s.db))

	// GET /settings/profile
	settings.GET("/profile", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		user, err := userRepo.GetByID(c.Request().Context(), userCtx.ID)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "User not found"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": user})
	})

	// PUT /settings/profile
	settings.PUT("/profile", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		var req struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request"})
		}
		user, err := userRepo.GetByID(c.Request().Context(), userCtx.ID)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "User not found"})
		}
		if req.Email != "" {
			user.Email = req.Email
		}
		if err := userRepo.Update(c.Request().Context(), user); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to update profile"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": user})
	})

	// POST /settings/change-password
	settings.POST("/change-password", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		var req struct {
			CurrentPassword string `json:"current_password"`
			NewPassword     string `json:"new_password"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request"})
		}
		user, err := userRepo.GetByID(c.Request().Context(), userCtx.ID)
		if err != nil {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "User not found"})
		}
		// Verify current password
		if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.CurrentPassword)); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Current password is incorrect"})
		}
		// Hash new password
		hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to hash password"})
		}
		if err := userRepo.UpdatePassword(c.Request().Context(), userCtx.ID, string(hash)); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to update password"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Password changed successfully"})
	})

	// POST /settings/2fa/setup
	settings.POST("/2fa/setup", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		if userService == nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "2FA service unavailable"})
		}
		// Generate a real TOTP secret + QR + backup codes (encrypted at rest).
		resp, err := userService.Setup2FA(&service.Setup2FARequest{UserID: userCtx.ID})
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"data": map[string]interface{}{
				"secret":       resp.Secret,
				"otp_url":      resp.QRCodeURL,
				"qr_code_png":  resp.QRCodePNG,
				"backup_codes": resp.BackupCodes,
			},
		})
	})

	// POST /settings/2fa/verify
	settings.POST("/2fa/verify", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		var req struct {
			Code string `json:"code"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request"})
		}
		if userService == nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "2FA service unavailable"})
		}
		// Verify the TOTP code against the real encrypted secret.
		if err := userService.Verify2FASetup(&service.Verify2FASetupRequest{UserID: userCtx.ID, TOTPCode: req.Code}); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "2FA enabled"})
	})

	// POST /settings/2fa/disable
	settings.POST("/2fa/disable", func(c echo.Context) error {
		userCtx, ok := panelMiddleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Not authenticated"})
		}
		if userService == nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "2FA service unavailable"})
		}
		if err := userService.Disable2FA(userCtx.ID); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "2FA disabled"})
	})

	// System settings (admin-only): one JSON row per section in system_settings.
	sys := settings.Group("/system")
	sys.Use(panelMiddleware.RequirePermission("admin:access"))
	sys.GET("", func(c echo.Context) error {
		type settingRow struct {
			Section string
			Data    string
		}
		var rows []settingRow
		if err := s.db.WithContext(c.Request().Context()).
			Raw("SELECT section, data::text AS data FROM system_settings").Scan(&rows).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to load settings"})
		}
		out := map[string]json.RawMessage{}
		for _, r := range rows {
			out[r.Section] = json.RawMessage(r.Data)
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
	})
	sys.PUT("", func(c echo.Context) error {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(c.Request().Body).Decode(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}
		for section, data := range body {
			if err := s.db.WithContext(c.Request().Context()).Exec(
				"INSERT INTO system_settings (section, data, updated_at) VALUES (?, ?::jsonb, NOW()) "+
					"ON CONFLICT (section) DO UPDATE SET data = EXCLUDED.data, updated_at = NOW()",
				section, string(data),
			).Error; err != nil {
				return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to save settings"})
			}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Settings saved"})
	})

	// POST /settings/system/email/test — send a test email with the given SMTP
	// settings (the form's current values) to verify they actually work.
	sys.POST("/email/test", func(c echo.Context) error {
		var body struct {
			SMTPHost     string `json:"smtpHost"`
			SMTPPort     int    `json:"smtpPort"`
			SMTPUser     string `json:"smtpUser"`
			SMTPPassword string `json:"smtpPassword"`
			SMTPFrom     string `json:"smtpFrom"`
			SMTPFromName string `json:"smtpFromName"`
			To           string `json:"to"`
		}
		if err := c.Bind(&body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}
		to := body.To
		if to == "" {
			if u, ok := panelMiddleware.GetUserContext(c); ok {
				to = u.Email
			}
		}
		if err := service.SendTestEmail(service.SMTPSettings{
			Host:     body.SMTPHost,
			Port:     body.SMTPPort,
			Username: body.SMTPUser,
			Password: body.SMTPPassword,
			From:     body.SMTPFrom,
			FromName: body.SMTPFromName,
		}, to); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Test email sent to " + to})
	})
}

// setupNotificationRoutes configures notification routes (uses audit_logs as source)
func (s *Server) setupNotificationRoutes(g *echo.Group) {
	auditRepo := repository.NewAuditRepository(s.db)

	notifications := g.Group("/notifications")
	notifications.Use(panelMiddleware.RequireAuth(s.db))

	// GET /notifications
	notifications.GET("", func(c echo.Context) error {
		ctx := c.Request().Context()
		logs, _ := auditRepo.List(ctx, 20, 0)
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"data":    logs,
		})
	})

	// PUT /notifications/:id/read
	notifications.PUT("/:id/read", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"success": true,
			"message": "Notification marked as read",
		})
	})
}

// Start starts the HTTP server
func (s *Server) Start() error {
	s.SetupRoutes()

	address := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)

	// Start the background node-metrics collector (persists samples for history).
	collectorCtx, stopCollector := context.WithCancel(context.Background())
	defer stopCollector()
	go service.NewMetricsCollector(s.db, s.riverClient, 60*time.Second, 7*24*time.Hour, slog.Default()).Run(collectorCtx)

	// Start the backup scheduler: starts the cron and (re)loads active schedules
	// from the DB so they survive restarts. Without this, scheduled backups never run.
	if s.backupService != nil {
		if err := s.backupService.Start(); err != nil {
			slog.Default().Error("failed to start backup scheduler", "error", err)
		}
	}

	// Start server in a goroutine
	go func() {
		if err := s.echo.Start(address); err != nil && err != http.ErrServerClosed {
			s.echo.Logger.Fatalf("shutting down the server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shutdown the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	stopCollector()
	if s.backupService != nil {
		s.backupService.Stop()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.echo.Shutdown(ctx)
}

// Run initializes and starts the panel server
func Run() error {
	// Load configuration (validated so missing required fields fail fast)
	cfg, err := config.LoadDefaultValidated()
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Initialize database
	dbCfg := repository.NewDBConfig(&cfg.Database)
	db, err := repository.InitDB(dbCfg)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}

	// Auto-apply SQL migrations
	if err := runMigrations(databaseURL(cfg.Database)); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Initialize River queue before routes are registered so services receive a non-nil client.
	queueClient, err := queue.NewClient(queue.DefaultConfig(databaseURL(cfg.Database)), slog.Default())
	if err != nil {
		return fmt.Errorf("failed to initialize queue: %w", err)
	}
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 30*time.Second)
	if err := queue.RunMigrationsWithLogger(migrateCtx, queueClient.Pool(), slog.Default()); err != nil {
		migrateCancel()
		_ = queueClient.Stop(context.Background())
		return fmt.Errorf("failed to run queue migrations: %w", err)
	}
	migrateCancel()
	if err := queueClient.Start(context.Background()); err != nil {
		_ = queueClient.Stop(context.Background())
		return fmt.Errorf("failed to start queue: %w", err)
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		if err := queueClient.Stop(shutdownCtx); err != nil {
			slog.Default().Error("failed to stop queue", "error", err)
		}
	}()

	// Create and start server
	server := NewServer(db, cfg)
	server.SetQueueClient(queueClient)
	fmt.Printf("Panel server starting on %s:%d\n", cfg.Server.Host, cfg.Server.Port)

	return server.Start()
}

// handleListManagedNetworks returns the GET /api/v1/networks handler as a
// testable method. The standalone managed-network inventory is the admin-defined
// bridge/NAT/isolated topology. A client holds network:read (which would
// otherwise admit them), so an explicit admin role is required to prevent
// topology leakage. The role decision lives here (rather than only in the
// permission middleware) so it can be unit-tested directly.
func (s *Server) handleListManagedNetworks(repo *repository.ManagedNetworkRepository) echo.HandlerFunc {
	return func(c echo.Context) error {
		if u, ok := panelMiddleware.GetUserContext(c); !ok || u.Role != models.RoleAdmin {
			return c.JSON(http.StatusForbidden, map[string]interface{}{
				"error":   "Forbidden",
				"message": "only administrators may list managed networks",
			})
		}
		nets, err := repo.List(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list networks"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nets})
	}
}
