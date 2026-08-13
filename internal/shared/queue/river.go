// Package queue provides PostgreSQL-based job queue using River
// This implementation uses pgx/v5/pgxpool for database connections
// and River for job queue management with multiple worker queues.
package queue

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Config holds the configuration for the queue system
type Config struct {
	// Database connection string
	DatabaseURL string

	// Maximum database connections (0 = default, typically 4x num CPU)
	MaxConns int32

	// Worker configuration
	CriticalWorkers int // VM lifecycle operations
	DefaultWorkers  int // General operations
	BatchWorkers    int // Backups, imports
	AuditWorkers    int // Audit logging

	// Job reschedule delay (how long to wait before retry)
	RescueStuckJobsAfter time.Duration
}

// DefaultConfig returns a default configuration
func DefaultConfig(databaseURL string) *Config {
	return &Config{
		DatabaseURL:          databaseURL,
		MaxConns:             20,
		CriticalWorkers:      20,
		DefaultWorkers:       50,
		BatchWorkers:         10,
		AuditWorkers:         20,
		RescueStuckJobsAfter: 1 * time.Hour,
	}
}

// Client wraps the River client and database pool
type Client struct {
	pool        *pgxpool.Pool
	gormDB      *gorm.DB
	riverClient *river.Client[pgx.Tx]
	logger      *slog.Logger
	config      *Config
	auditRepo   *repository.AuditRepository
	metrics     *MetricsCollector
	agentClient *AgentClient
}

// NewClient creates a new queue client with database pool and River client
func NewClient(config *Config, logger *slog.Logger) (*Client, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.DatabaseURL == "" {
		return nil, fmt.Errorf("database URL is required")
	}
	if logger == nil {
		logger = slog.Default()
	}

	client := &Client{
		config: config,
		logger: logger,
	}

	// Initialize database pool
	if err := client.initPool(context.Background()); err != nil {
		return nil, fmt.Errorf("failed to initialize database pool: %w", err)
	}

	// Initialize River client
	if err := client.initRiver(); err != nil {
		client.pool.Close()
		return nil, fmt.Errorf("failed to initialize River client: %w", err)
	}

	return client, nil
}

// initPool creates the database connection pool
func (c *Client) initPool(ctx context.Context) error {
	pgxConfig, err := pgxpool.ParseConfig(c.config.DatabaseURL)
	if err != nil {
		return fmt.Errorf("failed to parse database URL: %w", err)
	}

	// Configure connection pool
	if c.config.MaxConns > 0 {
		pgxConfig.MaxConns = c.config.MaxConns
	}

	// Set reasonable defaults for connection pool
	pgxConfig.MinConns = 5
	pgxConfig.MaxConnLifetime = 1 * time.Hour
	pgxConfig.MaxConnIdleTime = 30 * time.Minute
	pgxConfig.HealthCheckPeriod = 5 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, pgxConfig)
	if err != nil {
		return fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Test connection
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return fmt.Errorf("failed to ping database: %w", err)
	}

	c.pool = pool

	// Initialize GORM DB for repositories using same connection string
	gormDB, err := gorm.Open(postgres.Open(c.config.DatabaseURL), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		pool.Close()
		return fmt.Errorf("failed to create GORM connection: %w", err)
	}
	c.gormDB = gormDB

	c.logger.Info("database pool initialized",
		"max_conns", pgxConfig.MaxConns,
		"min_conns", pgxConfig.MinConns,
	)

	return nil
}

// initRiver initializes the River client with workers
func (c *Client) initRiver() error {
	workers := river.NewWorkers()

	c.metrics = NewMetricsCollector()
	c.agentClient = NewAgentClient()

	c.auditRepo = repository.NewAuditRepository(c.gormDB)
	templateRepo := repository.NewTemplateRepository(c.gormDB)
	nodeRepo := repository.NewNodeRepository(c.gormDB)
	vmRepo := repository.NewVMRepository(c.gormDB)

	SetWorkerContext(&WorkerContext{
		DB:           c.gormDB,
		VMRepo:       vmRepo,
		NodeRepo:     nodeRepo,
		TemplateRepo: templateRepo,
		NetworkRepo:  repository.NewNetworkRepository(c.gormDB),
		IPAMRepo:     repository.NewIPAMRepository(c.gormDB),
		AgentClient:  c.agentClient,
		Metrics:      c.metrics,
	})

	if err := c.gormDB.AutoMigrate(&JobRecord{}); err != nil {
		return fmt.Errorf("failed to migrate job records table: %w", err)
	}

	river.AddWorker(workers, NewVMOperationWorker(c.logger))
	river.AddWorker(workers, NewTemplateSyncWorker(c.logger, templateRepo, nodeRepo, c.gormDB))
	river.AddWorker(workers, NewBackupWorker(c.logger))
	river.AddWorker(workers, NewImageWorker(c.logger))
	river.AddWorker(workers, NewRestoreWorker(c.logger))
	river.AddWorker(workers, NewSnapshotWorker(c.logger))
	river.AddWorker(workers, NewImportWorker(c.logger))
	river.AddWorker(workers, NewNetworkConfigWorker(c.logger))
	river.AddWorker(workers, NewAuditWorker(c.logger, c.auditRepo))

	riverClient, err := river.NewClient(riverpgxv5.New(c.pool), &river.Config{
		Queues: map[string]river.QueueConfig{
			QueueCritical: {MaxWorkers: c.config.CriticalWorkers},
			QueueDefault:  {MaxWorkers: c.config.DefaultWorkers},
			QueueBatch:    {MaxWorkers: c.config.BatchWorkers},
			QueueAudit:    {MaxWorkers: c.config.AuditWorkers},
		},
		Workers:              workers,
		Logger:               c.logger,
		RescueStuckJobsAfter: c.config.RescueStuckJobsAfter,
	})
	if err != nil {
		return fmt.Errorf("failed to create River client: %w", err)
	}

	c.riverClient = riverClient
	c.logger.Info("River client initialized",
		"critical_workers", c.config.CriticalWorkers,
		"default_workers", c.config.DefaultWorkers,
		"batch_workers", c.config.BatchWorkers,
		"audit_workers", c.config.AuditWorkers,
	)

	return nil
}

// Start starts the River client and begins processing jobs
func (c *Client) Start(ctx context.Context) error {
	if c.riverClient == nil {
		return fmt.Errorf("River client not initialized")
	}

	c.logger.Info("starting River client")
	if err := c.riverClient.Start(ctx); err != nil {
		return fmt.Errorf("failed to start River client: %w", err)
	}

	c.logger.Info("River client started successfully")
	return nil
}

// StopAndCancel shuts down River, cancelling any jobs still running.
//
// Cancelling rather than waiting is deliberate. A job here can legitimately run
// for hours — a compressed disk export of a large VM is measured in hours, not
// minutes — so waiting would hang shutdown until the deploy timed out and the
// container was killed anyway.
//
// The alternative, which is what used to happen, is worse: the process simply
// exited and left the job's row marked `running` with no client behind it.
// River's rescuer will not reclaim such a row until the worker's own timeout has
// elapsed, so raising the image/backup timeouts to eight hours (necessary, since
// the export genuinely takes that long) meant an orphaned capture sat at
// "pending" for eight hours. Cancelling puts it straight back on the queue,
// where the next process picks it up.
func (c *Client) StopAndCancel(ctx context.Context) error {
	c.logger.Info("stopping queue client, cancelling in-flight jobs")
	if c.riverClient != nil {
		if err := c.riverClient.StopAndCancel(ctx); err != nil {
			c.logger.Error("failed to stop River client", "error", err)
		}
		c.riverClient = nil
	}
	if c.agentClient != nil {
		if err := c.agentClient.Close(); err != nil {
			c.logger.Error("failed to close agent client", "error", err)
		}
		c.agentClient = nil
	}
	if c.pool != nil {
		c.pool.Close()
		c.pool = nil
	}
	return nil
}

// Stop gracefully shuts down the River client and database pool
// Must be called to ensure graceful shutdown
func (c *Client) Stop(ctx context.Context) error {
	c.logger.Info("stopping queue client")

	// Stop River client first (to finish processing jobs)
	if c.riverClient != nil {
		c.logger.Info("stopping River client")
		if err := c.riverClient.Stop(ctx); err != nil {
			c.logger.Error("failed to stop River client", "error", err)
		}
		c.riverClient = nil
	}

	if c.agentClient != nil {
		c.logger.Info("closing agent client connections")
		if err := c.agentClient.Close(); err != nil {
			c.logger.Error("failed to close agent client", "error", err)
		}
		c.agentClient = nil
	}

	// Close database pool
	if c.pool != nil {
		c.logger.Info("closing database pool")
		c.pool.Close()
		c.pool = nil
	}

	c.logger.Info("queue client stopped")
	return nil
}

func (c *Client) GetMetrics() (processed, failed, retried, dead int64, avgLatency time.Duration) {
	if c.metrics == nil {
		return 0, 0, 0, 0, 0
	}
	return c.metrics.GetMetrics()
}

func (c *Client) ResetMetrics() {
	if c.metrics != nil {
		c.metrics = NewMetricsCollector()
	}
}

// InsertJob inserts a job into the queue
func (c *Client) InsertJob(ctx context.Context, job river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if c.riverClient == nil {
		return nil, fmt.Errorf("River client not initialized")
	}

	return c.riverClient.Insert(ctx, job, opts)
}

// InsertJobTx inserts a job into the queue within a transaction
func (c *Client) InsertJobTx(ctx context.Context, tx pgx.Tx, job river.JobArgs, opts *river.InsertOpts) (*rivertype.JobInsertResult, error) {
	if c.riverClient == nil {
		return nil, fmt.Errorf("River client not initialized")
	}

	return c.riverClient.InsertTx(ctx, tx, job, opts)
}

// Pool returns the underlying database pool (for direct database access)
func (c *Client) Pool() *pgxpool.Pool {
	return c.pool
}

// RiverClient returns the underlying River client (for advanced operations)
func (c *Client) RiverClient() *river.Client[pgx.Tx] {
	return c.riverClient
}

// Stats is disabled - River doesn't expose this directly
// Use database queries to get queue statistics if needed
func (c *Client) Stats(ctx context.Context) error {
	if c.riverClient == nil {
		return fmt.Errorf("River client not initialized")
	}
	return nil
}

// RunMigrations runs River migrations to create required tables
// This should be called during application startup before starting the client
func RunMigrations(ctx context.Context, pool *pgxpool.Pool) error {
	return RunMigrationsWithLogger(ctx, pool, slog.Default())
}

// RunMigrationsWithLogger runs River migrations with a custom logger
func RunMigrationsWithLogger(ctx context.Context, pool *pgxpool.Pool, logger *slog.Logger) error {
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return fmt.Errorf("failed to create migrator: %w", err)
	}

	_, err = migrator.Migrate(ctx, rivermigrate.DirectionUp, &rivermigrate.MigrateOpts{})
	if err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}

	logger.Info("River migrations completed successfully")
	return nil
}

// VerifyTables checks if River tables exist in the database
func VerifyTables(ctx context.Context, pool *pgxpool.Pool) error {
	requiredTables := []string{
		"river_job",
		"river_leader",
		"river_queue",
	}

	for _, table := range requiredTables {
		var exists bool
		err := pool.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM information_schema.tables 
				WHERE table_schema = 'public' AND table_name = $1
			)
		`, table).Scan(&exists)
		if err != nil {
			return fmt.Errorf("failed to check table %s: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("required table missing: %s", table)
		}
	}

	return nil
}

// SimpleInsert helpers for common operations

// InsertVMOperation inserts a VM operation job
func (c *Client) InsertVMOperation(ctx context.Context, job VMOperationJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}

// InsertTemplateSync inserts a template sync job
func (c *Client) InsertTemplateSync(ctx context.Context, job TemplateSyncJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}

// InsertBackup inserts a backup job
func (c *Client) InsertBackup(ctx context.Context, job BackupJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}

// InsertImport inserts an import job
func (c *Client) InsertImport(ctx context.Context, job ImportJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}

func (c *Client) InsertAudit(ctx context.Context, job AuditJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}

func (c *Client) InsertNetworkConfig(ctx context.Context, job NetworkConfigJob) (*rivertype.JobInsertResult, error) {
	return c.riverClient.Insert(ctx, job, nil)
}
