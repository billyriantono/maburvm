package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/maburvm/panel/internal/panel/handler"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// Server represents the HTTP server
type Server struct {
	echo *echo.Echo
	db   *gorm.DB
	cfg  *config.Config
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

	// Snapshot routes
	s.setupSnapshotRoutes()

	// Backup routes
	s.setupBackupRoutes()

	// Import routes
	s.setupImportRoutes()

	// User management routes
	s.setupUserRoutes(v1)

	// Audit log routes
	s.setupAuditLogRoutes(v1)

	// Billing webhook routes (outside /api group, uses own auth)
	s.setupBillingRoutes()
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
	auth.GET("/me", authHandler.Me, panelMiddleware.RequireAuth(s.db))
}

// setupNodeRoutes configures node-related routes
func (s *Server) setupNodeRoutes(g *echo.Group) {
	// Initialize repository
	nodeRepo := repository.NewNodeRepository(s.db)

	// Initialize service
	nodeService := service.NewNodeService(nodeRepo)

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
}

// setupVMRoutes configures VM-related routes
func (s *Server) setupVMRoutes(g *echo.Group) {
	logger := slog.Default()

	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)

	vmService := service.NewVMService(s.db, vmRepo, nodeRepo, templateRepo, nil, logger)

	wsHost := ""
	if s.cfg.Server.Host != "" {
		wsHost = fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	}

	vncService := service.NewVNCService(s.db, vmRepo, nodeRepo, logger, s.cfg.JWT.SecretKey, wsHost)

	if err := vncService.Migrate(); err != nil {
		logger.Error("failed to migrate console tokens table", "error", err)
	}

	vmHandler := handler.NewVMHandler(vmService, vncService)

	handler.RegisterVMRoutes(s.echo, vmHandler, s.db)
}

// setupStorageRoutes configures storage-related routes
func (s *Server) setupStorageRoutes(g *echo.Group) {
	storageRepo := repository.NewStorageRepository(s.db)
	storageService := service.NewStorageService(storageRepo)
	storageHandler := handler.NewStorageHandler(storageService)

	storage := g.Group("/storage")
	storage.Use(panelMiddleware.RequireAuth(s.db))

	storage.GET("/pools", storageHandler.GetPools)
	storage.GET("/pools/:id", storageHandler.GetPoolByID)
	storage.POST("/pools", storageHandler.CreatePool, panelMiddleware.RequirePermission("storage:create"))
	storage.PUT("/pools/:id", storageHandler.UpdatePool, panelMiddleware.RequirePermission("storage:update"))
	storage.DELETE("/pools/:id", storageHandler.DeletePool, panelMiddleware.RequirePermission("storage:delete"))

	storage.GET("/pools/:id/volumes", storageHandler.GetVolumes)
	storage.POST("/pools/:id/volumes", storageHandler.CreateVolume, panelMiddleware.RequirePermission("storage:create"))
	storage.DELETE("/pools/:poolId/volumes/:volumeId", storageHandler.DeleteVolume, panelMiddleware.RequirePermission("storage:delete"))
}

// setupTemplateRoutes configures template-related routes
func (s *Server) setupTemplateRoutes(g *echo.Group) {
	templateRepo := repository.NewTemplateRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateService := service.NewTemplateService(s.db, templateRepo, nodeRepo, nil)
	templateHandler := handler.NewTemplateHandler(templateService)
	templateHandler.RegisterRoutes(s.echo, panelMiddleware.RequireAuth(s.db))
}

// setupNetworkRoutes configures network-related routes
func (s *Server) setupNetworkRoutes() {
	networkRepo := repository.NewNetworkRepository(s.db)
	firewallRepo := repository.NewFirewallRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	networkService := service.NewNetworkService(s.db, networkRepo, firewallRepo, vmRepo, nodeRepo, nil)
	networkHandler := handler.NewNetworkHandler(networkService)
	handler.RegisterNetworkRoutes(s.echo, networkHandler, s.db)
}

// setupSnapshotRoutes configures snapshot-related routes
func (s *Server) setupSnapshotRoutes() {
	logger := slog.Default()
	snapshotRepo := repository.NewSnapshotRepository(s.db)
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	snapshotService := service.NewSnapshotService(s.db, snapshotRepo, vmRepo, nodeRepo, nil, logger)
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
	backupService := service.NewBackupService(s.db, backupRepo, scheduleRepo, vmRepo, nodeRepo, nil, nil, logger)
	backupHandler := handler.NewBackupHandler(backupService)
	handler.RegisterBackupRoutes(s.echo, backupHandler, s.db)
}

// setupImportRoutes configures VM import routes
func (s *Server) setupImportRoutes() {
	logger := slog.Default()
	vmRepo := repository.NewVMRepository(s.db)
	nodeRepo := repository.NewNodeRepository(s.db)
	templateRepo := repository.NewTemplateRepository(s.db)
	importService := service.NewImportService(s.db, vmRepo, nodeRepo, templateRepo, nil, logger)
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

	vmService := service.NewVMService(s.db, vmRepo, nodeRepo, templateRepo, nil, logger)

	billingHandler := handler.NewBillingHandler(vmService, logger)
	handler.RegisterBillingRoutes(s.echo, billingHandler)
}

// Start starts the HTTP server
func (s *Server) Start() error {
	s.SetupRoutes()

	address := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)

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

	// Create and start server
	server := NewServer(db, cfg)
	fmt.Printf("Panel server starting on %s:%d\n", cfg.Server.Host, cfg.Server.Port)

	return server.Start()
}
