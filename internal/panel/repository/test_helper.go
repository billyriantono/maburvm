package repository

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestRepository provides database access for tests using SQLite
type TestRepository struct {
	DB *gorm.DB
}

// SetupTestDB creates a test database with proper schema
func SetupTestDB() (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, err
	}

	// Create tables with SQLite-compatible schema
	createTables := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT DEFAULT 'client',
quota_mode TEXT NOT NULL DEFAULT 'legacy',
					two_factor_secret TEXT,
			two_factor_backup_codes TEXT,
			ip_whitelist TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			token_revoked_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS nodes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			ip_address TEXT NOT NULL,
			status TEXT DEFAULT 'offline',
			token TEXT UNIQUE NOT NULL,
			cert_fingerprint TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS os_templates (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			version TEXT NOT NULL,
			image_path TEXT NOT NULL,
			is_active BOOLEAN DEFAULT 1,
			description TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS vms (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			node_id TEXT NOT NULL,
			hostname TEXT NOT NULL,
			os_template_id TEXT NOT NULL,
			resources TEXT,
			status TEXT DEFAULT 'stopped',
			source_migration TEXT,
			vnc_port INTEGER,
			vnc_password TEXT,
			console_enabled BOOLEAN DEFAULT 1,
			rescue_mode BOOLEAN DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS networks (
			id TEXT PRIMARY KEY,
			vm_id TEXT NOT NULL,
			ip_address TEXT NOT NULL,
			bandwidth_limit INTEGER DEFAULT 0,
			bandwidth_quota_gb INTEGER DEFAULT 0,
			over_quota_policy TEXT DEFAULT 'throttle',
			throttle_speed_mbps INTEGER DEFAULT 0,
			throttled BOOLEAN DEFAULT 0,
			vlan_id INTEGER,
			anti_spoofing BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS firewall_rules (
			id TEXT PRIMARY KEY,
			vm_id TEXT NOT NULL,
			protocol TEXT NOT NULL,
			port_range TEXT,
			action TEXT NOT NULL,
			direction TEXT NOT NULL,
			source_ip TEXT DEFAULT '0.0.0.0/0',
			priority INTEGER DEFAULT 100,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS audit_logs (
			id TEXT PRIMARY KEY,
			user_id TEXT,
			action TEXT NOT NULL,
			resource_type TEXT,
			resource_id TEXT,
			ip_address TEXT,
			user_agent TEXT,
			details TEXT,
			before_snapshot TEXT,
			after_snapshot TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			token TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			ip_address TEXT,
			user_agent TEXT,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS backups (
			id TEXT PRIMARY KEY,
			vm_id TEXT NOT NULL,
			file_path TEXT NOT NULL,
			size INTEGER,
			status TEXT DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			deleted_at DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id TEXT PRIMARY KEY,
			vm_id TEXT NOT NULL,
			name TEXT NOT NULL,
			description TEXT,
			status TEXT DEFAULT 'pending',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			completed_at DATETIME,
			deleted_at DATETIME
		)`,
	}

	for _, sql := range createTables {
		if err := db.Exec(sql).Error; err != nil {
			return nil, err
		}
	}

	return db, nil
}

// CleanupTestDB drops all tables
func CleanupTestDB(db *gorm.DB) error {
	tables := []string{
		"snapshots", "backups", "sessions", "audit_logs",
		"firewall_rules", "networks", "vms", "os_templates", "nodes", "users",
	}
	for _, table := range tables {
		if err := db.Exec("DROP TABLE IF EXISTS " + table).Error; err != nil {
			return err
		}
	}
	return nil
}

// BaseTestSuite provides common test functionality
type BaseTestSuite struct {
	suite.Suite
	DB *gorm.DB
}

func (suite *BaseTestSuite) SetupSuite() {
	var err error
	suite.DB, err = SetupTestDB()
	if err != nil {
		suite.T().Fatalf("Failed to setup test database: %v", err)
	}
}

func (suite *BaseTestSuite) TearDownSuite() {
	sqlDB, err := suite.DB.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func (suite *BaseTestSuite) SetupTest() {
	// Clean all tables
	tables := []string{
		"audit_logs", "sessions", "snapshots", "backups",
		"firewall_rules", "networks", "vms", "os_templates", "nodes", "users",
	}
	for _, table := range tables {
		suite.DB.Exec("DELETE FROM " + table)
	}
}

// TestSetupTestDB verifies the test database setup
func TestSetupTestDB(t *testing.T) {
	db, err := SetupTestDB()
	assert.NoError(t, err)
	assert.NotNil(t, db)

	// Verify tables exist
	tables := []string{"users", "nodes", "vms", "networks", "firewall_rules", "os_templates", "audit_logs"}
	for _, table := range tables {
		err = db.Exec("SELECT 1 FROM " + table + " LIMIT 1").Error
		assert.NoError(t, err, "Table %s should exist", table)
	}

	sqlDB, _ := db.DB()
	sqlDB.Close()
}

// TestCleanupTestDB verifies the test database cleanup
func TestCleanupTestDB(t *testing.T) {
	db, err := SetupTestDB()
	assert.NoError(t, err)

	err = CleanupTestDB(db)
	assert.NoError(t, err)

	sqlDB, _ := db.DB()
	sqlDB.Close()
}
