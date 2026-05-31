package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

// UserRepositoryTestSuite tests UserRepository
type UserRepositoryTestSuite struct {
	BaseTestSuite
	userRepo *UserRepository
}

func (suite *UserRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.userRepo = NewUserRepository(suite.DB)
}

func TestUserRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(UserRepositoryTestSuite))
}

func (suite *UserRepositoryTestSuite) TestNewUserRepository() {
	assert.NotNil(suite.T(), suite.userRepo)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByID() {
	ctx := context.Background()

	user := &models.User{
		Email:        "test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	found, err := suite.userRepo.GetByID(ctx, user.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.Email, found.Email)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_GetByEmail() {
	ctx := context.Background()

	user := &models.User{
		Email:        "email-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	found, err := suite.userRepo.GetByEmail(ctx, user.Email)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), user.ID, found.ID)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_List() {
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "list" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	users, err := suite.userRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), users, 5)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_ListByRole() {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		user := &models.User{
			Email:        "admin" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleAdmin,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "client" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	admins, err := suite.userRepo.ListByRole(ctx, models.RoleAdmin, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), admins, 3)

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
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Update() {
	ctx := context.Background()

	user := &models.User{
		Email:        "update@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	user.Role = models.RoleAdmin
	err = suite.userRepo.Update(ctx, user)
	assert.NoError(suite.T(), err)

	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.RoleAdmin, found.Role)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Delete() {
	ctx := context.Background()

	user := &models.User{
		Email:        "delete@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	err = suite.userRepo.Delete(ctx, user.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.User{}).Where("id = ?", user.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_Count() {
	ctx := context.Background()

	count, err := suite.userRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		user := &models.User{
			Email:        "count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.userRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_CountByRole() {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		user := &models.User{
			Email:        "admin-count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleAdmin,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 7; i++ {
		user := &models.User{
			Email:        "client-count" + string(rune('0'+i)) + "@example.com",
			PasswordHash: "hashedpassword",
			Role:         models.RoleClient,
		}
		err := suite.DB.Create(user).Error
		assert.NoError(suite.T(), err)
	}

	adminCount, err := suite.userRepo.CountByRole(ctx, models.RoleAdmin)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), adminCount)

	clientCount, err := suite.userRepo.CountByRole(ctx, models.RoleClient)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(7), clientCount)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_EmailExists() {
	ctx := context.Background()

	exists, err := suite.userRepo.EmailExists(ctx, "nonexistent@example.com")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	user := &models.User{
		Email:        "exists@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err = suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	exists, err = suite.userRepo.EmailExists(ctx, "exists@example.com")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdatePassword() {
	ctx := context.Background()

	user := &models.User{
		Email:        "pass@example.com",
		PasswordHash: "oldpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	newHash := "newhashedpassword"
	err = suite.userRepo.UpdatePassword(ctx, user.ID, newHash)
	assert.NoError(suite.T(), err)

	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newHash, found.PasswordHash)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdateTwoFactorSecret() {
	ctx := context.Background()

	user := &models.User{
		Email:        "2fa@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	secret := "secretkey123"
	err = suite.userRepo.UpdateTwoFactorSecret(ctx, user.ID, secret)
	assert.NoError(suite.T(), err)

	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), secret, found.TwoFactorSecret)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_ClearTwoFactorSecret() {
	ctx := context.Background()

	user := &models.User{
		Email:           "2fa-clear@example.com",
		PasswordHash:    "hashedpassword",
		Role:            models.RoleClient,
		TwoFactorSecret: "secretkey123",
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	err = suite.userRepo.ClearTwoFactorSecret(ctx, user.ID)
	assert.NoError(suite.T(), err)

	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Empty(suite.T(), found.TwoFactorSecret)
}

func (suite *UserRepositoryTestSuite) TestUserRepository_UpdateIPWhitelist() {
	ctx := context.Background()

	user := &models.User{
		Email:        "ip@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.DB.Create(user).Error
	assert.NoError(suite.T(), err)

	whitelist := []string{"192.168.1.1", "10.0.0.1"}
	err = suite.userRepo.UpdateIPWhitelist(ctx, user.ID, whitelist)
	assert.NoError(suite.T(), err)

	var found models.User
	err = suite.DB.First(&found, "id = ?", user.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), whitelist, found.IPWhitelist)
}
