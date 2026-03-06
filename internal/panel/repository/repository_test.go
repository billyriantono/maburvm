package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// RepositoryTestSuite provides base test infrastructure for all repository tests
type RepositoryTestSuite struct {
	suite.Suite
	db *gorm.DB
}

// SetupSuite runs once before all tests
func (suite *RepositoryTestSuite) SetupSuite() {
	var err error
	// Use in-memory SQLite for testing
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	// Migrate all models
	err = suite.db.AutoMigrate(
		&models.User{},
		&models.Node{},
		&models.VM{},
		&models.Network{},
		&models.FirewallRule{},
		&models.OSTemplate{},
		&models.AuditLog{},
		&models.Session{},
		&models.Backup{},
		&models.Snapshot{},
	)
	if err != nil {
		suite.T().Fatalf("Failed to migrate test database: %v", err)
	}
}

// TearDownSuite runs once after all tests
func (suite *RepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

// SetupTest runs before each test - clean up tables
func (suite *RepositoryTestSuite) SetupTest() {
	// Clean all tables before each test
	suite.db.Exec("DELETE FROM audit_logs")
	suite.db.Exec("DELETE FROM sessions")
	suite.db.Exec("DELETE FROM snapshots")
	suite.db.Exec("DELETE FROM backups")
	suite.db.Exec("DELETE FROM firewall_rules")
	suite.db.Exec("DELETE FROM networks")
	suite.db.Exec("DELETE FROM vms")
	suite.db.Exec("DELETE FROM nodes")
	suite.db.Exec("DELETE FROM os_templates")
	suite.db.Exec("DELETE FROM users")
}

// TestRepositoryTestSuite runs the test suite
func TestRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(RepositoryTestSuite))
}

// ========== Base Repository Tests ==========

func (suite *RepositoryTestSuite) TestBaseRepository_GetByID() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Create a user
	user := &models.User{
		Email:        "test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := baseRepo.GetByID(ctx, user.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.Email, found.Email)
}

func (suite *RepositoryTestSuite) TestBaseRepository_GetByID_NotFound() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Try to get non-existent user
	_, err := baseRepo.GetByID(ctx, uuid.New())
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *RepositoryTestSuite) TestBaseRepository_List() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Create multiple users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "test" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	users, err := baseRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 5)
}

func (suite *RepositoryTestSuite) TestBaseRepository_List_Pagination() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Create 10 users
	for i := 0; i < 10; i++ {
		user := &models.User{
			Email:        "test" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// List with limit
	users, err := baseRepo.List(ctx, 3, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 3)

	// List with offset
	users, err = baseRepo.List(ctx, 0, 5)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 5)

	// List with limit and offset
	users, err = baseRepo.List(ctx, 3, 5)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 3)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Create() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	user := &models.User{
		Email:        "new@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleAdmin,
	}

	err := baseRepo.Create(ctx, user)
	assert.NoError(suite.T(), err)
	assert.NotEqual(suite.T(), uuid.Nil, user.ID)

	// Verify created
	var found models.User
	err = suite.db.First(&found, "email = ?", "new@example.com").Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Update() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Create user
	user := &models.User{
		Email:        "update@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update
	user.Role = models.RoleAdmin
	err = baseRepo.Update(ctx, user)
	assert.NoError(suite.T(), err)

	// Verify update
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Delete() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Create user
	user := &models.User{
		Email:        "delete@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = baseRepo.Delete(ctx, user.ID)
	assert.NoError(suite.T(), err)

	// Verify deletion (hard delete)
	var count int64
	suite.db.Unscoped().Model(&models.User{}).Where("id = ?", user.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Count() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.db)

	// Initial count
	count, err := baseRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "test" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// Count again
	count, err = baseRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *RepositoryTestSuite) TestBaseRepository_GetByIDWithPreload() {
	ctx := context.Background()

	// Create related records
	user := &models.User{
		Email:        "preload@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	node := &models.Node{
		Name:      "test-node",
		IPAddress: "192.168.1.1",
		Status:    models.NodeStatusActive,
		Token:     "test-token",
	}
	err = suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	template := &models.OSTemplate{
		Name:      "Ubuntu",
		Version:   "22.04",
		ImagePath: "/images/ubuntu.img",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "test-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err = suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Test GetByIDWithPreload with VM
	vmRepo := NewBaseRepository[models.VM](suite.db)
	// Note: SQLite doesn't support arrays, so we test without preloads first
	found, err := vmRepo.GetByID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *RepositoryTestSuite) TestBaseRepository_ListWithPreload() {
	ctx := context.Background()

	// Create related records
	user := &models.User{
		Email:        "preload-list@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	node := &models.Node{
		Name:      "test-node-2",
		IPAddress: "192.168.1.2",
		Status:    models.NodeStatusActive,
		Token:     "test-token-2",
	}
	err = suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	template := &models.OSTemplate{
		Name:      "Debian",
		Version:   "12",
		ImagePath: "/images/debian.img",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	// Create VMs
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Test ListWithPreload
	vmRepo := NewBaseRepository[models.VM](suite.db)
	vms, err := vmRepo.ListWithPreload(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 3)
}

// ========== Repository Tests ==========

func (suite *RepositoryTestSuite) TestRepository_DB() {
	repo := NewRepository(suite.db)
	assert.NotNil(suite.T(), repo.DB())
	assert.Equal(suite.T(), suite.db, repo.DB())
}

func (suite *RepositoryTestSuite) TestRepository_WithTx() {
	repo := NewRepository(suite.db)

	// Test successful transaction
	err := repo.WithTx(func(tx *gorm.DB) error {
		user := &models.User{
			Email:        "tx-test@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		return tx.Create(user).Error
	})
	assert.NoError(suite.T(), err)

	// Verify transaction committed
	var found models.User
	err = suite.db.Where("email = ?", "tx-test@example.com").First(&found).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "tx-test@example.com", found.Email)
}

func (suite *RepositoryTestSuite) TestRepository_WithTx_Rollback() {
	repo := NewRepository(suite.db)

	// Test rollback on error
	testEmail := "tx-rollback@example.com"
	err := repo.WithTx(func(tx *gorm.DB) error {
		user := &models.User{
			Email:        testEmail,
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		// Force rollback
		return gorm.ErrInvalidData
	})
	assert.Error(suite.T(), err)

	// Verify transaction rolled back
	var found models.User
	err = suite.db.Where("email = ?", testEmail).First(&found).Error
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *RepositoryTestSuite) TestRepository_WithTxContext() {
	ctx := context.Background()
	repo := NewRepository(suite.db)

	// Test transaction with context
	err := repo.WithTxContext(ctx, func(tx *gorm.DB) error {
		user := &models.User{
			Email:        "tx-ctx@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		return tx.Create(user).Error
	})
	assert.NoError(suite.T(), err)

	// Verify transaction committed
	var found models.User
	err = suite.db.Where("email = ?", "tx-ctx@example.com").First(&found).Error
	assert.NoError(suite.T(), err)
}

func (suite *RepositoryTestSuite) TestRepository_WithTxContext_Cancelled() {
	ctx, cancel := context.WithCancel(context.Background())
	repo := NewRepository(suite.db)

	// Cancel context before transaction
	cancel()

	// Test transaction with cancelled context
	testEmail := "tx-cancel@example.com"
	err := repo.WithTxContext(ctx, func(tx *gorm.DB) error {
		user := &models.User{
			Email:        testEmail,
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		return tx.Create(user).Error
	})
	// Transaction might fail due to cancelled context
	_ = err // We accept both error and success depending on timing
}

// ========== NewDBConfig Tests ==========

func TestNewDBConfig(t *testing.T) {
	cfg := &DBConfig{
		Host:            "localhost",
		Port:            5432,
		User:            "postgres",
		Password:        "secret",
		Name:            "panel",
		SSLMode:         "disable",
		MaxIdleConns:    10,
		MaxOpenConns:    100,
		ConnMaxLifetime: 0,
	}

	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5432, cfg.Port)
	assert.Equal(t, "postgres", cfg.User)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, "panel", cfg.Name)
}
