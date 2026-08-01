// Package repository provides database access layer for the panel
package repository

import (
	"context"
	"fmt"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/config"
)

// DBConfig holds database connection configuration
type DBConfig struct {
	Host            string
	Port            int
	User            string
	Password        string
	Name            string
	SSLMode         string
	MaxIdleConns    int
	MaxOpenConns    int
	ConnMaxLifetime time.Duration
}

// NewDBConfig creates DBConfig from application config
func NewDBConfig(cfg *config.DatabaseConfig) *DBConfig {
	return &DBConfig{
		Host:            cfg.Host,
		Port:            cfg.Port,
		User:            cfg.User,
		Password:        cfg.Password,
		Name:            cfg.Name,
		SSLMode:         cfg.SSLMode,
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: time.Hour,
	}
}

// DatabaseURL builds the PostgreSQL connection string for this config via the
// shared, URL-encoded helper in the config package, so it escapes special
// characters in credentials identically to migrations and the River queue.
func (c *DBConfig) DatabaseURL() string {
	return config.DatabaseConfig{
		Host:     c.Host,
		Port:     c.Port,
		User:     c.User,
		Password: c.Password,
		Name:     c.Name,
		SSLMode:  c.SSLMode,
	}.DatabaseURL()
}

// Repository provides base repository functionality
type Repository struct {
	db *gorm.DB
}

// NewRepository creates a new Repository instance
func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

// DB returns the underlying GORM database connection
func (r *Repository) DB() *gorm.DB {
	return r.db
}

// InitDB initializes the database connection with connection pool settings
func InitDB(cfg *DBConfig) (*gorm.DB, error) {
	// Build the DSN through the shared, URL-encoded config helper so special
	// characters in the password are escaped correctly (spaces, quotes, '@',
	// ':', '/', '?', '#', backslashes) and the behavior matches migrations/River.
	dsn := cfg.DatabaseURL()

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to database: %w", err)
	}

	// Configure connection pool
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(cfg.ConnMaxLifetime)

	return db, nil
}

// WithTx executes the given function within a transaction
func (r *Repository) WithTx(fn func(*gorm.DB) error) error {
	return r.db.Transaction(fn)
}

// WithTxContext executes the given function within a transaction with context
func (r *Repository) WithTxContext(ctx context.Context, fn func(*gorm.DB) error) error {
	return r.db.WithContext(ctx).Transaction(fn)
}

// BaseRepository provides generic CRUD operations for any model
type BaseRepository[T any] struct {
	db *gorm.DB
}

// NewBaseRepository creates a new BaseRepository instance
func NewBaseRepository[T any](db *gorm.DB) *BaseRepository[T] {
	return &BaseRepository[T]{db: db}
}

// GetByID retrieves a record by its ID
func (r *BaseRepository[T]) GetByID(ctx context.Context, id interface{}) (*T, error) {
	var entity T
	if err := r.db.WithContext(ctx).First(&entity, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// GetByIDWithPreload retrieves a record by its ID with specified associations preloaded
func (r *BaseRepository[T]) GetByIDWithPreload(ctx context.Context, id interface{}, preloads ...string) (*T, error) {
	var entity T
	query := r.db.WithContext(ctx)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if err := query.First(&entity, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &entity, nil
}

// List retrieves all records with optional pagination
func (r *BaseRepository[T]) List(ctx context.Context, limit, offset int) ([]T, error) {
	var entities []T
	query := r.db.WithContext(ctx)
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// ListWithPreload retrieves all records with specified associations preloaded
func (r *BaseRepository[T]) ListWithPreload(ctx context.Context, limit, offset int, preloads ...string) ([]T, error) {
	var entities []T
	query := r.db.WithContext(ctx)
	for _, preload := range preloads {
		query = query.Preload(preload)
	}
	if limit > 0 {
		query = query.Limit(limit)
	}
	if offset > 0 {
		query = query.Offset(offset)
	}
	if err := query.Find(&entities).Error; err != nil {
		return nil, err
	}
	return entities, nil
}

// Create inserts a new record
func (r *BaseRepository[T]) Create(ctx context.Context, entity *T) error {
	return r.db.WithContext(ctx).Create(entity).Error
}

// Update updates an existing record
func (r *BaseRepository[T]) Update(ctx context.Context, entity *T) error {
	return r.db.WithContext(ctx).Save(entity).Error
}

// Delete removes a record by ID (hard delete as per PRD compliance requirements)
func (r *BaseRepository[T]) Delete(ctx context.Context, id interface{}) error {
	var entity T
	return r.db.WithContext(ctx).Unscoped().Delete(&entity, "id = ?", id).Error
}

// Count returns the total number of records
func (r *BaseRepository[T]) Count(ctx context.Context) (int64, error) {
	var count int64
	var entity T
	if err := r.db.WithContext(ctx).Model(&entity).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}
