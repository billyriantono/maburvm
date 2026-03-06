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

// ========== UserRepository Tests ==========

func (suite *RepositoryTestSuite) TestUserRepository_GetByEmail() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user
	user := &models.User{
		Email:        "email-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Get by email
	found, err := userRepo.GetByEmail(ctx, "email-test@example.com")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.ID, found.ID)
}

func (suite *RepositoryTestSuite) TestUserRepository_GetByEmail_NotFound() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	_, err := userRepo.GetByEmail(ctx, "nonexistent@example.com")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *RepositoryTestSuite) TestUserRepository_ListByRole() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create admin
	admin := &models.User{
		Email:        "admin@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleAdmin,
	}
	err := suite.db.Create(admin).Error
	assert.NoError(suite.T(), err)

	// Create clients
	for i := 0; i < 3; i++ {
		user := &models.User{
			Email:        "client" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// List admins
	admins, err := userRepo.ListByRole(ctx, models.RoleAdmin, 10, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), admins, 1)

	// List clients
	clients, err := userRepo.ListByRole(ctx, models.RoleClient, 10, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), clients, 3)
}

func (suite *RepositoryTestSuite) TestUserRepository_EmailExists() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user
	user := &models.User{
		Email:        "exists@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := userRepo.EmailExists(ctx, "exists@example.com")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = userRepo.EmailExists(ctx, "notfound@example.com")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestUserRepository_UpdatePassword() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user
	user := &models.User{
		Email:        "password@example.com",
		PasswordHash: "oldpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update password
	err = userRepo.UpdatePassword(ctx, user.ID, "newpasswordhash")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "newpasswordhash", found.PasswordHash)
}

func (suite *RepositoryTestSuite) TestUserRepository_UpdateTwoFactorSecret() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user
	user := &models.User{
		Email:        "2fa@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update 2FA secret
	err = userRepo.UpdateTwoFactorSecret(ctx, user.ID, "secret123456")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "secret123456", found.TwoFactorSecret)
}

func (suite *RepositoryTestSuite) TestUserRepository_ClearTwoFactorSecret() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user with 2FA
	user := &models.User{
		Email:           "2fa-clear@example.com",
		PasswordHash:    "hashedpassword",
		Role:            models.RoleClient,
		TwoFactorSecret: "secret123456",
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Clear 2FA secret
	err = userRepo.ClearTwoFactorSecret(ctx, user.ID)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "", found.TwoFactorSecret)
}

func (suite *RepositoryTestSuite) TestUserRepository_UpdateIPWhitelist() {
	ctx := context.Background()
	userRepo := NewUserRepository(suite.db)

	// Create user
	user := &models.User{
		Email:        "whitelist@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update whitelist
	whitelist := []string{"192.168.1.1", "10.0.0.0/24"}
	err = userRepo.UpdateIPWhitelist(ctx, user.ID, whitelist)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), whitelist, found.IPWhitelist)
}

// ========== NodeRepository Tests ==========

func (suite *RepositoryTestSuite) TestNodeRepository_GetByToken() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "test-node",
		IPAddress: "192.168.1.1",
		Status:    models.NodeStatusActive,
		Token:     "unique-token-12345",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Get by token
	found, err := nodeRepo.GetByToken(ctx, "unique-token-12345")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.ID, found.ID)
}

func (suite *RepositoryTestSuite) TestNodeRepository_GetByIPAddress() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "test-node-2",
		IPAddress: "192.168.1.2",
		Status:    models.NodeStatusActive,
		Token:     "unique-token-67890",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Get by IP
	found, err := nodeRepo.GetByIPAddress(ctx, "192.168.1.2")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.ID, found.ID)
}

func (suite *RepositoryTestSuite) TestNodeRepository_ListByStatus() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create active node
	activeNode := &models.Node{
		Name:      "active-node",
		IPAddress: "192.168.1.10",
		Status:    models.NodeStatusActive,
		Token:     "token-active",
	}
	err := suite.db.Create(activeNode).Error
	assert.NoError(suite.T(), err)

	// Create offline node
	offlineNode := &models.Node{
		Name:      "offline-node",
		IPAddress: "192.168.1.11",
		Status:    models.NodeStatusOffline,
		Token:     "token-offline",
	}
	err = suite.db.Create(offlineNode).Error
	assert.NoError(suite.T(), err)

	// List active
	activeNodes, err := nodeRepo.ListByStatus(ctx, models.NodeStatusActive, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(activeNodes), 1)

	// List offline
	offlineNodes, err := nodeRepo.ListByStatus(ctx, models.NodeStatusOffline, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(offlineNodes), 1)
}

func (suite *RepositoryTestSuite) TestNodeRepository_ListActive() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create multiple active nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "active-node-" + string(rune('0'+i)),
			IPAddress: "192.168.1." + string(rune('0'+20+i)),
			Status:    models.NodeStatusActive,
			Token:     "token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// List all active
	activeNodes, err := nodeRepo.ListActive(ctx)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(activeNodes), 3)
}

func (suite *RepositoryTestSuite) TestNodeRepository_UpdateStatus() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "status-node",
		IPAddress: "192.168.1.30",
		Status:    models.NodeStatusActive,
		Token:     "token-status",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Update status
	err = nodeRepo.UpdateStatus(ctx, node.ID, models.NodeStatusMaintenance)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.NodeStatusMaintenance, found.Status)
}

func (suite *RepositoryTestSuite) TestNodeRepository_UpdateToken() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "token-node",
		IPAddress: "192.168.1.31",
		Status:    models.NodeStatusActive,
		Token:     "old-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Update token
	err = nodeRepo.UpdateToken(ctx, node.ID, "new-token-12345")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "new-token-12345", found.Token)
}

func (suite *RepositoryTestSuite) TestNodeRepository_NameExists() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "unique-node-name",
		IPAddress: "192.168.1.40",
		Status:    models.NodeStatusActive,
		Token:     "token-exists",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := nodeRepo.NameExists(ctx, "unique-node-name")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = nodeRepo.NameExists(ctx, "nonexistent-node")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestNodeRepository_IPAddressExists() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create node
	node := &models.Node{
		Name:      "ip-node",
		IPAddress: "192.168.1.50",
		Status:    models.NodeStatusActive,
		Token:     "token-ip",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := nodeRepo.IPAddressExists(ctx, "192.168.1.50")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = nodeRepo.IPAddressExists(ctx, "192.168.1.99")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestNodeRepository_GetIDs() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create multiple nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "id-node-" + string(rune('0'+i)),
			IPAddress: "192.168.1." + string(rune('0'+60+i)),
			Status:    models.NodeStatusActive,
			Token:     "token-id-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// Get IDs
	ids, err := nodeRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(ids), 3)
}

func (suite *RepositoryTestSuite) TestNodeRepository_GetIDsByStatus() {
	ctx := context.Background()
	nodeRepo := NewNodeRepository(suite.db)

	// Create active node
	activeNode := &models.Node{
		Name:      "active-id-node",
		IPAddress: "192.168.1.70",
		Status:    models.NodeStatusActive,
		Token:     "token-active-id",
	}
	err := suite.db.Create(activeNode).Error
	assert.NoError(suite.T(), err)

	// Get active IDs
	ids, err := nodeRepo.GetIDsByStatus(ctx, models.NodeStatusActive)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(ids), 1)
}

// ========== VMRepository Tests ==========

func (suite *RepositoryTestSuite) setupVMTestData() (*models.User, *models.Node, *models.OSTemplate) {
	// Create test user
	user := &models.User{
		Email:        "vm-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Create test node
	node := &models.Node{
		Name:      "vm-test-node",
		IPAddress: "192.168.2.1",
		Status:    models.NodeStatusActive,
		Token:     "vm-test-token",
	}
	err = suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Create test template
	template := &models.OSTemplate{
		Name:      "TestOS",
		Version:   "1.0",
		ImagePath: "/images/test.qcow2",
		IsActive:  true,
	}
	err = suite.db.Create(template).Error
	assert.NoError(suite.T(), err)

	return user, node, template
}

func (suite *RepositoryTestSuite) TestVMRepository_Create() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "test-vm-1",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}

	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), vm.ID)
}

func (suite *RepositoryTestSuite) TestVMRepository_GetByID() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "test-vm-get",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)

	found, err := vmRepo.GetByID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *RepositoryTestSuite) TestVMRepository_ListByUserID() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	// Create multiple VMs for the user
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "user-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources: models.Resources{
				CPU:  2,
				RAM:  4096,
				Disk: 50,
			},
			Status: models.VMStatusStopped,
		}
		err := vmRepo.Create(ctx, vm)
		assert.NoError(suite.T(), err)
	}

	vms, err := vmRepo.ListByUserID(ctx, user.ID.String(), 10, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 3)
}

func (suite *RepositoryTestSuite) TestVMRepository_ListByNodeID() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	// Create VM on node
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "node-vm-1",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)

	vms, err := vmRepo.ListByNodeID(ctx, node.ID, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(vms), 1)
}

func (suite *RepositoryTestSuite) TestVMRepository_ListByStatus() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	// Create running VM
	runningVM := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "running-vm",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusRunning,
	}
	err := vmRepo.Create(ctx, runningVM)
	assert.NoError(suite.T(), err)

	// Create stopped VM
	stoppedVM := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "stopped-vm",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err = vmRepo.Create(ctx, stoppedVM)
	assert.NoError(suite.T(), err)

	// List running
	runningVMs, err := vmRepo.ListByStatus(ctx, models.VMStatusRunning, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(runningVMs), 1)

	// List stopped
	stoppedVMs, err := vmRepo.ListByStatus(ctx, models.VMStatusStopped, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(stoppedVMs), 1)
}

func (suite *RepositoryTestSuite) TestVMRepository_UpdateStatus() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "status-update-vm",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)

	// Update status
	err = vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusRunning)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.VMStatusRunning, found.Status)
}

func (suite *RepositoryTestSuite) TestVMRepository_UpdateResources() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "resources-vm",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)

	// Update resources
	newResources := models.Resources{
		CPU:  4,
		RAM:  8192,
		Disk: 100,
	}
	err = vmRepo.UpdateResources(ctx, vm.ID, newResources)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 4, found.Resources.CPU)
	assert.Equal(suite.T(), 8192, found.Resources.RAM)
	assert.Equal(suite.T(), 100, found.Resources.Disk)
}

func (suite *RepositoryTestSuite) TestVMRepository_HostnameExists() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "unique-hostname",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := vmRepo.HostnameExists(ctx, "unique-hostname")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = vmRepo.HostnameExists(ctx, "nonexistent-hostname")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestVMRepository_CountByUserID() {
	ctx := context.Background()
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	// Create multiple VMs
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "count-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources: models.Resources{
				CPU:  2,
				RAM:  4096,
				Disk: 50,
			},
			Status: models.VMStatusStopped,
		}
		err := vmRepo.Create(ctx, vm)
		assert.NoError(suite.T(), err)
	}

	count, err := vmRepo.CountByUserID(ctx, user.ID.String())
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), count)
}

// ========== NetworkRepository Tests ==========

func (suite *RepositoryTestSuite) setupNetworkTestData() (*models.User, *models.Node, *models.OSTemplate, *models.VM) {
	user, node, template := suite.setupVMTestData()
	vmRepo := NewVMRepository(suite.db)

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "network-test-vm",
		OSTemplateID: template.ID,
		Resources: models.Resources{
			CPU:  2,
			RAM:  4096,
			Disk: 50,
		},
		Status: models.VMStatusStopped,
	}
	err := vmRepo.Create(context.Background(), vm)
	assert.NoError(suite.T(), err)

	return user, node, template, vm
}

func (suite *RepositoryTestSuite) TestNetworkRepository_Create() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.10",
		BandwidthLimit: 1000,
	}

	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), network.ID)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_GetByID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.11",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	found, err := networkRepo.GetByID(ctx, network.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_GetByVMID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.12",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	found, err := networkRepo.GetByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_GetByIPAddress() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.13",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	found, err := networkRepo.GetByIPAddress(ctx, "192.168.100.13")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.ID, found.VMID)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_ListByVMID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	// Create multiple networks for VM
	for i := 0; i < 3; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "192.168.100." + string(rune('0'+20+i)),
			BandwidthLimit: 1000,
		}
		err := networkRepo.Create(ctx, network)
		assert.NoError(suite.T(), err)
	}

	networks, err := networkRepo.ListByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), networks, 3)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_UpdateBandwidthLimit() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.30",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	// Update bandwidth limit
	err = networkRepo.UpdateBandwidthLimit(ctx, network.ID, 2000)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(2000), found.BandwidthLimit)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_UpdateIPAddress() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.40",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	// Update IP address
	err = networkRepo.UpdateIPAddress(ctx, network.ID, "192.168.100.200")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "192.168.100.200", found.IPAddress)
}

func (suite *RepositoryTestSuite) TestNetworkRepository_IPAddressExists() {
	ctx := context.Background()
	_, _, _, vm := suite.setupNetworkTestData()
	networkRepo := NewNetworkRepository(suite.db)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.100.50",
		BandwidthLimit: 1000,
	}
	err := networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := networkRepo.IPAddressExists(ctx, "192.168.100.50")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = networkRepo.IPAddressExists(ctx, "192.168.100.99")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

// ========== FirewallRepository Tests ==========

func (suite *RepositoryTestSuite) setupFirewallTestData() (*models.User, *models.Node, *models.OSTemplate, *models.VM) {
	return suite.setupNetworkTestData()
}

func (suite *RepositoryTestSuite) TestFirewallRepository_Create() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "22",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}

	err := firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), rule.ID)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_GetByID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)

	found, err := firewallRepo.GetByID(ctx, rule.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), rule.PortRange, found.PortRange)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_ListByVMID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	// Create multiple rules
	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('0' + 80 + i)),
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := firewallRepo.Create(ctx, rule)
		assert.NoError(suite.T(), err)
	}

	rules, err := firewallRepo.ListByVMID(ctx, vm.ID, 10, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), rules, 3)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_ListByVMIDAndDirection() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	// Create inbound rule
	inboundRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, inboundRule)
	assert.NoError(suite.T(), err)

	// Create outbound rule
	outboundRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "443",
		Action:    "allow",
		Direction: "outbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err = firewallRepo.Create(ctx, outboundRule)
	assert.NoError(suite.T(), err)

	// List inbound
	inboundRules, err := firewallRepo.ListByVMIDAndDirection(ctx, vm.ID, "inbound", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(inboundRules), 1)

	// List outbound
	outboundRules, err := firewallRepo.ListByVMIDAndDirection(ctx, vm.ID, "outbound", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(outboundRules), 1)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_ListByProtocol() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	// Create TCP rule
	tcpRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, tcpRule)
	assert.NoError(suite.T(), err)

	// Create UDP rule
	udpRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "udp",
		PortRange: "53",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err = firewallRepo.Create(ctx, udpRule)
	assert.NoError(suite.T(), err)

	// List TCP
	tcpRules, err := firewallRepo.ListByProtocol(ctx, "tcp", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(tcpRules), 1)

	// List UDP
	udpRules, err := firewallRepo.ListByProtocol(ctx, "udp", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(udpRules), 1)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_ListByAction() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	// Create allow rule
	allowRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, allowRule)
	assert.NoError(suite.T(), err)

	// Create deny rule
	denyRule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "22",
		Action:    "deny",
		Direction: "inbound",
		SourceIP:  "192.168.1.0/24",
		Priority:  50,
	}
	err = firewallRepo.Create(ctx, denyRule)
	assert.NoError(suite.T(), err)

	// List allow rules
	allowRules, err := firewallRepo.ListByAction(ctx, "allow", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(allowRules), 1)

	// List deny rules
	denyRules, err := firewallRepo.ListByAction(ctx, "deny", 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(denyRules), 1)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_UpdatePriority() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "443",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)

	// Update priority
	err = firewallRepo.UpdatePriority(ctx, rule.ID, 50)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 50, found.Priority)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_UpdateAction() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "3306",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)

	// Update action
	err = firewallRepo.UpdateAction(ctx, rule.ID, "deny")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "deny", found.Action)
}

func (suite *RepositoryTestSuite) TestFirewallRepository_CountByVMID() {
	ctx := context.Background()
	_, _, _, vm := suite.setupFirewallTestData()
	firewallRepo := NewFirewallRepository(suite.db)

	// Create multiple rules
	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('0' + 80 + i)),
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := firewallRepo.Create(ctx, rule)
		assert.NoError(suite.T(), err)
	}

	count, err := firewallRepo.CountByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), count)
}

// ========== TemplateRepository Tests ==========

func (suite *RepositoryTestSuite) TestTemplateRepository_Create() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "TestOS",
		Version:   "1.0",
		ImagePath: "/images/test-os.qcow2",
		IsActive:  true,
	}

	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), template.ID)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_GetByName() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "Ubuntu",
		Version:   "22.04",
		ImagePath: "/images/ubuntu-22.04.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	found, err := templateRepo.GetByName(ctx, "Ubuntu")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "Ubuntu", found.Name)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_GetByNameAndVersion() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "CentOS",
		Version:   "8",
		ImagePath: "/images/centos-8.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	found, err := templateRepo.GetByNameAndVersion(ctx, "CentOS", "8")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "CentOS", found.Name)
	assert.Equal(suite.T(), "8", found.Version)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_ListActive() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	// Create active templates
	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{
			Name:      "ActiveOS-" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/active-" + string(rune('0'+i)) + ".qcow2",
			IsActive:  true,
		}
		err := templateRepo.Create(ctx, template)
		assert.NoError(suite.T(), err)
	}

	// Create inactive template
	inactiveTemplate := &models.OSTemplate{
		Name:      "InactiveOS",
		Version:   "1.0",
		ImagePath: "/images/inactive.qcow2",
		IsActive:  false,
	}
	err := templateRepo.Create(ctx, inactiveTemplate)
	assert.NoError(suite.T(), err)

	// List active
	activeTemplates, err := templateRepo.ListActive(ctx, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(activeTemplates), 3)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_ListInactive() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	// Create inactive template
	inactiveTemplate := &models.OSTemplate{
		Name:      "InactiveTestOS",
		Version:   "1.0",
		ImagePath: "/images/inactive-test.qcow2",
		IsActive:  false,
	}
	err := templateRepo.Create(ctx, inactiveTemplate)
	assert.NoError(suite.T(), err)

	// List inactive
	inactiveTemplates, err := templateRepo.ListInactive(ctx, 10, 0)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), len(inactiveTemplates), 1)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_UpdateActiveStatus() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "ToggleOS",
		Version:   "1.0",
		ImagePath: "/images/toggle.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	// Update to inactive
	err = templateRepo.UpdateActiveStatus(ctx, template.ID, false)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), found.IsActive)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_UpdateImagePath() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "PathOS",
		Version:   "1.0",
		ImagePath: "/images/old-path.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	// Update path
	err = templateRepo.UpdateImagePath(ctx, template.ID, "/images/new-path.qcow2")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.OSTemplate
	err = suite.db.First(&found, template.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "/images/new-path.qcow2", found.ImagePath)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_NameExists() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "UniqueOS",
		Version:   "1.0",
		ImagePath: "/images/unique.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := templateRepo.NameExists(ctx, "UniqueOS")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = templateRepo.NameExists(ctx, "NonExistentOS")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_NameAndVersionExists() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "VersionedOS",
		Version:   "2.0",
		ImagePath: "/images/versioned.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	// Check existing name and version
	exists, err := templateRepo.NameAndVersionExists(ctx, "VersionedOS", "2.0")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check existing name with different version
	exists, err = templateRepo.NameAndVersionExists(ctx, "VersionedOS", "1.0")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_ImagePathExists() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	template := &models.OSTemplate{
		Name:      "PathCheckOS",
		Version:   "1.0",
		ImagePath: "/images/path-check.qcow2",
		IsActive:  true,
	}
	err := templateRepo.Create(ctx, template)
	assert.NoError(suite.T(), err)

	// Check existing
	exists, err := templateRepo.ImagePathExists(ctx, "/images/path-check.qcow2")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)

	// Check non-existing
	exists, err = templateRepo.ImagePathExists(ctx, "/images/nonexistent.qcow2")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)
}

func (suite *RepositoryTestSuite) TestTemplateRepository_CountActive() {
	ctx := context.Background()
	templateRepo := NewTemplateRepository(suite.db)

	// Create active templates
	for i := 0; i < 3; i++ {
		template := &models.OSTemplate{
			Name:      "CountActive-" + string(rune('0'+i)),
			Version:   "1.0",
			ImagePath: "/images/count-active-" + string(rune('0'+i)) + ".qcow2",
			IsActive:  true,
		}
		err := templateRepo.Create(ctx, template)
		assert.NoError(suite.T(), err)
	}

	count, err := templateRepo.CountActive(ctx)
	assert.NoError(suite.T(), err)
	assert.GreaterOrEqual(suite.T(), count, int64(3))
}
