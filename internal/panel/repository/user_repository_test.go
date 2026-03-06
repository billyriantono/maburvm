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

// UserRepositoryTestSuite tests UserRepository
type UserRepositoryTestSuite struct {
	suite.Suite
	db       *gorm.DB
	userRepo *UserRepository
}

func (suite *UserRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.User{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.userRepo = NewUserRepository(suite.db)
}

func (suite *UserRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM users")
}

func (suite *UserRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestUserRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(UserRepositoryTestSuite))
}

func (suite *UserRepositoryTestSuite) TestNewUserRepository() {
	assert.NotNil(suite.T(), suite.userRepo)
	assert.NotNil(suite.T(), suite.userRepo.base)
	assert.NotNil(suite.T(), suite.userRepo.db)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByID() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.userRepo.GetByID(ctx, user.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.Email, found.Email)
	assert.Equal(suite.T(), user.Role, found.Role)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByID_NotFound() {
	ctx := context.Background()

	_, err := suite.userRepo.GetByID(ctx, uuid.New())
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByEmail() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "email-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Get by email
	found, err := suite.userRepo.GetByEmail(ctx, user.Email)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.ID, found.ID)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByEmail_NotFound() {
	ctx := context.Background()

	_, err := suite.userRepo.GetByEmail(ctx, "nonexistent@example.com")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_List() {
	ctx := context.Background()

	// Create users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "list" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	users, err := suite.userRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 5)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_List_Pagination() {
	ctx := context.Background()

	// Create users
	for i := 0; i < 10; i++ {
		user := &models.User{
			Email:        "page" + string(rune('0'+i%10)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// Test pagination
	users, err := suite.userRepo.List(ctx, 3, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 3)

	users, err = suite.userRepo.List(ctx, 0, 5)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 5)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_ListByRole() {
	ctx := context.Background()

	// Create admin users
	for i := 0; i < 3; i++ {
		user := &models.User{
			Email:        "admin" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleAdmin,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// Create client users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "client" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// List admins
	admins, err := suite.userRepo.ListByRole(ctx, models.RoleAdmin, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), admins, 3)

	// List clients
	clients, err := suite.userRepo.ListByRole(ctx, models.RoleClient, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), clients, 5)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Create() {
	ctx := context.Background()

	user := &models.User{
		Email:        "new@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}

	err := suite.userRepo.Create(ctx, user)
	assert.NoError(suite.T(), err)
	assert.NotEqual(suite.T(), uuid.Nil, user.ID)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.Email, found.Email)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Update() {
	ctx := context.Background()

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
	err = suite.userRepo.Update(ctx, user)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Delete() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "delete@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.userRepo.Delete(ctx, user.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.User{}).Where("id = ?", user.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Count() {
	ctx := context.Background()

	// Initial count
	count, err := suite.userRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.userRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_CountByRole() {
	ctx := context.Background()

	// Create mixed users
	for i := 0; i < 3; i++ {
		user := &models.User{
			Email:        "admin-count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleAdmin,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 7; i++ {
		user := &models.User{
			Email:        "client-count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.db.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// Count by role
	adminCount, err := suite.userRepo.CountByRole(ctx, models.RoleAdmin)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), adminCount)

	clientCount, err := suite.userRepo.CountByRole(ctx, models.RoleClient)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(7), clientCount)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_EmailExists() {
	ctx := context.Background()

	// Check non-existent email
	exists, err := suite.userRepo.EmailExists(ctx, "nonexistent@example.com")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create user
	user := &models.User{
		Email:        "exists@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Check existing email
	exists, err = suite.userRepo.EmailExists(ctx, "exists@example.com")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdatePassword() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "pass@example.com",
		PasswordHash: "oldpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update password
	newHash := "newhashedpassword"
	err = suite.userRepo.UpdatePassword(ctx, user.ID, newHash)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newHash, found.PasswordHash)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdateTwoFactorSecret() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "2fa@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update 2FA secret
	secret := "secretkey123"
	err = suite.userRepo.UpdateTwoFactorSecret(ctx, user.ID, secret)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), secret, found.TwoFactorSecret)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_ClearTwoFactorSecret() {
	ctx := context.Background()

	// Create user with 2FA
	user := &models.User{
		Email:           "2fa-clear@example.com",
		PasswordHash:    "hashedpassword",
		Role:            models.RoleClient,
		TwoFactorSecret: "secretkey123",
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Clear 2FA secret
	err = suite.userRepo.ClearTwoFactorSecret(ctx, user.ID)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), found.TwoFactorSecret)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdateIPWhitelist() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "ip@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update IP whitelist
	whitelist := []string{"192.168.1.1", "10.0.0.1"}
	err = suite.userRepo.UpdateIPWhitelist(ctx, user.ID, whitelist)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.User
	err = suite.db.First(&found, user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), whitelist, found.IPWhitelist)
}
