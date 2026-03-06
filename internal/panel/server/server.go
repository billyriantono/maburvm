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

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	"github.com/maburvm/panel/internal/panel/handler"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/config"
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
	e.Use(middleware.CORS())
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
	v1 := s.echo.Group("/api")

	// Node routes
	s.setupNodeRoutes(v1)

	// VM routes
	s.setupVMRoutes(v1)

	// Billing webhook routes (outside /api group, uses own auth)
	s.setupBillingRoutes()

	// TODO: Add other route groups (users, networks, etc.)
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
