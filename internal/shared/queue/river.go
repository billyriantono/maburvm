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
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"github.com/riverqueue/river/rivertype"
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
	riverClient *river.Client[pgx.Tx]
	logger      *slog.Logger
	config      *Config
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
	c.logger.Info("database pool initialized",
		"max_conns", pgxConfig.MaxConns,
		"min_conns", pgxConfig.MinConns,
	)

	return nil
}

// initRiver initializes the River client with workers
func (c *Client) initRiver() error {
	workers := river.NewWorkers()

	river.AddWorker(workers, NewVMOperationWorker(c.logger))
	river.AddWorker(workers, NewTemplateSyncWorker(c.logger))
	river.AddWorker(workers, NewBackupWorker(c.logger))
	river.AddWorker(workers, NewImportWorker(c.logger))
	river.AddWorker(workers, NewAuditWorker(c.logger))

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

// Stop gracefully shuts down the River client and database pool
// Must be called to ensure graceful shutdown
func (c *Client) Stop(ctx context.Context) error {
	c.logger.Info("stopping queue client")

	// Stop River client first (to finish processing jobs)
	if c.riverClient != nil {
		c.logger.Info("stopping River client")
		if err := c.riverClient.Stop(ctx); err != nil {
			c.logger.Error("failed to stop River client", "error", err)
			// Continue to close pool even if River stop fails
		}
		c.riverClient = nil
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
