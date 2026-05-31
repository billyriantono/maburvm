package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/riverqueue/river"
	"golang.org/x/crypto/bcrypt"

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
	echo        *echo.Echo
	db          *gorm.DB
	cfg         *config.Config
	queueClient *queue.Client
	riverClient *river.Client[pgx.Tx]
}

// NewServer creates a new HTTP server instance
func NewServer(db *gorm.DB, cfg *config.Config) *Server {
	e := echo.New()

	// Middleware
	e.Use(middleware.Logger())
	e.Use(middleware.Recover())
	e.Use(middleware.CORSWithConfig(middleware.CORSConfig{
		AllowOrigins: []string{
			"http://localhost:3000",
			"http://localhost:3001",
			"http://100.118.100.27:3000",
			"http://100.118.100.27:3001",
		},
		AllowMethods: []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions},
		AllowHeaders: []string{
			echo.HeaderOrigin,
			echo.HeaderContentType,
			echo.HeaderAccept,
			echo.HeaderAuthorization,
			echo.HeaderXRequestedWith,
		},
		AllowCredentials: true,
		MaxAge:           86400,
	}))
	e.Use(middleware.Secure())
	e.Use(middleware.BodyLimit("10M"))
	e.Use(middleware.RequestID())

	return &Server{
		echo: e,
		db:   db,
		cfg:  cfg,
	}
}

// SetQueueClient wires the River queue into HTTP services that enqueue asynchronous work.
func (s *Server) SetQueueClient(client *queue.Client) {
	s.queueClient = client
	if client != nil {
		s.riverClient = client.RiverClient()
	}
}

func databaseURL(cfg config.DatabaseConfig) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   cfg.Name,
	}
	q := u.Query()
	if cfg.SSLMode != "" {
		q.Set("sslmode", cfg.SSLMode)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// SetupRoutes configures all API routes
func (s *Server) SetupRoutes() {
	// Health check
	s.echo.GET("/health", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]interface{}{
			"status":    "healthy",
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})
	})

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

	// Recipe routes (per-user first-boot scripts, Virtualizor "Recipes")
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
	auth.POST("/login", authHandler.Login)
	auth.POST("/register", authHandler.Register)
	auth.POST("/logout", authHandler.Logout)
	auth.POST("/refresh", panelMiddleware.RefreshTokenHandler(s.db))
	auth.GET("/me", authHandler.Me, panelMiddleware.RequireAuth(s.db))
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

	vncService := service.NewVNCService(s.db, vmRepo, nodeRepo, logger, s.cfg.JWT.SecretKey, wsHost)

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

	vmHandler := handler.NewVMHandler(vmService, vncService, vncProxyServer, service.NewSSHKeyService(s.db), service.NewRecipeService(s.db))

	handler.RegisterVMRoutes(s.echo, vmHandler, s.db)

	// Bandwidth usage routes (nested under VMs)
	bwRepo := repository.NewBandwidthUsageRepository(s.db)
	bwService := service.NewBandwidthService(bwRepo, logger)
	bwHandler := handler.NewBandwidthHandler(bwService)

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
	storageHandler := handler.NewStorageHandler(storageService)

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
	networkHandler := handler.NewNetworkHandler(networkService)
	handler.RegisterNetworkRoutes(s.echo, networkHandler, s.db)

	// Standalone /api/v1/networks endpoint: administrator-defined virtual networks
	// (bridge/NAT/isolated) — persisted, not phantom. Per-VM IPs live under IP Pools.
	managedNetRepo := repository.NewManagedNetworkRepository(s.db)
	mnService := service.NewManagedNetworkService(s.db)
	networks := s.echo.Group("/api/v1/networks")
	networks.Use(panelMiddleware.RequireAuth(s.db))
	networks.GET("", func(c echo.Context) error {
		nets, err := managedNetRepo.List(c.Request().Context())
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list networks"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nets})
	})
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

// setupRecipeRoutes configures per-user first-boot recipe routes (Virtualizor
// "Recipes" — saved scripts applied to a VM on first boot via cloud-init).
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
	metricsHandler := handler.NewMetricsHandler(metricsService)
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
	snapshotHandler := handler.NewSnapshotHandler(snapshotService)
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
	handler.RegisterImportRoutes(s.echo, importHandler)
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
			Email       string          `json:"email"`
			Password    string          `json:"password"`
			Role        models.UserRole `json:"role"`
			IPWhitelist []string        `json:"ip_whitelist"`
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
		if exists, err := userRepo.EmailExists(c.Request().Context(), req.Email); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to check email"})
		} else if exists {
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Email already exists"})
		}

		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to hash password"})
		}
		user := &models.User{
			Email:        req.Email,
			PasswordHash: string(hash),
			Role:         req.Role,
			IPWhitelist:  req.IPWhitelist,
		}
		if err := userRepo.Create(c.Request().Context(), user); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to create user"})
		}
		return c.JSON(http.StatusCreated, map[string]interface{}{"data": user})
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
		var req struct {
			Email string `json:"email"`
			Role  string `json:"role"`
		}
		if err := c.Bind(&req); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
		}
		if req.Email != "" {
			user.Email = req.Email
		}
		if req.Role != "" {
			user.Role = models.UserRole(req.Role)
		}
		if err := userRepo.Update(c.Request().Context(), user); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to update user"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"data": user})
	}, panelMiddleware.RequirePermission("user:update"))

	users.DELETE("/:id", func(c echo.Context) error {
		id, err := parseUUID(c.Param("id"))
		if err != nil {
			return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid user ID"})
		}
		if err := userRepo.Delete(c.Request().Context(), id); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to delete user"})
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

		// Recent activity (last 10 audit logs)
		recentLogs, _ := auditRepo.List(ctx, 10, 0)

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
				"recent_activity": recentLogs,
			},
		})
	})
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

// setupBillingRoutes configures billing webhook routes with HMAC authentication
func (s *Server) setupBillingRoutes() {
	logger := slog.Default()

	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)

	vmService := service.NewVMService(s.db, vmRepo, nodeRepo, templateRepo, s.riverClient, logger)

	billingHandler := handler.NewBillingHandler(vmService, logger)
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
	go service.NewMetricsCollector(s.db, 60*time.Second, 7*24*time.Hour, slog.Default()).Run(collectorCtx)

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	return s.echo.Shutdown(ctx)
}

// Run initializes and starts the panel server
func Run() error {
	// Load configuration
	cfg, err := config.LoadDefault()
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
