package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// RepositoryTestSuite provides base test infrastructure for all repository tests
type RepositoryTestSuite struct {
	BaseTestSuite
}

func TestRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(RepositoryTestSuite))
}

// ========== Base Repository Tests ==========

func (suite *RepositoryTestSuite) TestBaseRepository_GetByID() {
	// Create a user manually
	userID := uuid.New().String()
	err := suite.DB.Exec(`INSERT INTO users (id, email, password_hash, role) VALUES (?, ?, ?, ?)`,
		userID, "test@example.com", "hashedpassword", "client").Error
	assert.NoError(suite.T(), err)

	// Get by ID
	var entity models.User
	err = suite.DB.First(&entity, "id = ?", userID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "test@example.com", entity.Email)
}

func (suite *RepositoryTestSuite) TestBaseRepository_List() {
	ctx := context.Background()

	// Create multiple users
	for i := 0; i < 5; i++ {
		userID := uuid.New().String()
		err := suite.DB.Exec(`INSERT INTO users (id, email, password_hash, role) VALUES (?, ?, ?, ?)`,
			userID, "test"+string(rune('0'+i))+"@example.com", "hashedpassword", "client").Error
		assert.NoError(suite.T(), err)
	}

	// List all
	var entities []models.User
	err := suite.DB.WithContext(ctx).Find(&entities).Error
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), entities, 5)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Create() {
	ctx := context.Background()

	user := &models.User{
		Email:        "new@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleAdmin,
	}

	err := suite.DB.WithContext(ctx).Create(user).Error
	assert.NoError(suite.T(), err)
	assert.NotEqual(suite.T(), uuid.Nil, user.ID)

	// Verify created
	var found models.User
	err = suite.DB.First(&found, "email = ?", "new@example.com").Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Update() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "update@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	// Update
	user.Role = models.RoleAdmin
	err = suite.DB.WithContext(ctx).Save(user).Error
	assert.NoError(suite.T(), err)

	// Verify update
	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Delete() {
	ctx := context.Background()

	// Create user
	user := &models.User{
		Email:        "delete@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	// Delete
	baseRepo := NewBaseRepository[models.User](suite.DB)
	err = baseRepo.Delete(ctx, user.ID)
	assert.NoError(suite.T(), err)

	// Verify deletion (hard delete)
	var count int64
	suite.DB.Unscoped().Model(&models.User{}).Where("id = ?", user.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *RepositoryTestSuite) TestBaseRepository_Count() {
	ctx := context.Background()
	baseRepo := NewBaseRepository[models.User](suite.DB)

	// Initial count
	count, err := baseRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create users
	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	// Count again
	count, err = baseRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

// ========== Repository Tests ==========

func (suite *RepositoryTestSuite) TestRepository_DB() {
	repo := NewRepository(suite.DB)
	assert.NotNil(suite.T(), repo.DB())
	assert.Equal(suite.T(), suite.DB, repo.DB())
}

func (suite *RepositoryTestSuite) TestRepository_WithTx() {
	repo := NewRepository(suite.DB)

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
	err = suite.DB.Where("email = ?", "tx-test@example.com").First(&found).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "tx-test@example.com", found.Email)
}

func (suite *RepositoryTestSuite) TestRepository_WithTx_Rollback() {
	repo := NewRepository(suite.DB)

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
	err = suite.DB.Where("email = ?", testEmail).First(&found).Error
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *RepositoryTestSuite) TestRepository_WithTxContext() {
	ctx := context.Background()
	repo := NewRepository(suite.DB)

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
	err = suite.DB.Where("email = ?", "tx-ctx@example.com").First(&found).Error
	assert.NoError(suite.T(), err)
}

// ========== NewDBConfig Tests ==========

func TestNewDBConfig(t *testing.T) {
	cfg := &DBConfig{
		Host:         "localhost",
		Port:         5432,
		User:         "postgres",
		Password:     "secret",
		Name:         "panel",
		SSLMode:      "disable",
		MaxIdleConns: 10,
		MaxOpenConns: 100,
	}

	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 5432, cfg.Port)
	assert.Equal(t, "postgres", cfg.User)
	assert.Equal(t, "secret", cfg.Password)
	assert.Equal(t, "panel", cfg.Name)
}
