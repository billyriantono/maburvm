package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type AuditRepositoryTestSuite struct {
	BaseTestSuite
	auditRepo *AuditRepository
}

func (suite *AuditRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.auditRepo = NewAuditRepository(suite.DB)
}

func TestAuditRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(AuditRepositoryTestSuite))
}

func (suite *AuditRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{Email: "audit-test@example.com", PasswordHash: "hashedpassword", Role: models.RoleClient}
	suite.DB.Create(user)
	return user
}

func (suite *AuditRepositoryTestSuite) TestNewAuditRepository() {
	assert.NotNil(suite.T(), suite.auditRepo)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_Create() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	auditLog := &models.AuditLog{
		UserID:       &userID,
		Action:       "CREATE_VM",
		ResourceType: "vm",
		ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
		IPAddress:    "192.168.1.100",
		UserAgent:    "Mozilla/5.0",
		Details:      map[string]any{"vm_name": "test-vm", "cpu": 2, "ram": 4096},
	}

	err := suite.auditRepo.Create(ctx, auditLog)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), auditLog.ID)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	auditLog := &models.AuditLog{
		UserID:       &userID,
		Action:       "DELETE_VM",
		ResourceType: "vm",
		ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
		IPAddress:    "192.168.1.100",
		Details:      map[string]any{"vm_id": "vm-123"},
	}
	err := suite.DB.Create(auditLog).Error
	assert.NoError(suite.T(), err)

	found, err := suite.auditRepo.GetByID(ctx, auditLog.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), auditLog.Action, found.Action)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	actions := []string{"CREATE_VM", "START_VM", "STOP_VM", "DELETE_VM", "UPDATE_VM"}
	for _, action := range actions {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       action,
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{"action": action},
		}
		err := suite.DB.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	logs, err := suite.auditRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 5)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_ListByUser() {
	ctx := context.Background()

	user1 := suite.createTestUser()
	user2 := &models.User{Email: "user2@example.com", PasswordHash: "hashedpassword", Role: models.RoleClient}
	suite.DB.Create(user2)

	user1ID := user1.ID.String()
	user2ID := user2.ID.String()

	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{UserID: &user1ID, Action: "ACTION", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.100", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	for i := 0; i < 2; i++ {
		auditLog := &models.AuditLog{UserID: &user2ID, Action: "ACTION", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.101", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	logs, err := suite.auditRepo.ListByUser(ctx, user1ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 3)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_ListByAction() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	for i := 0; i < 4; i++ {
		auditLog := &models.AuditLog{UserID: &userID, Action: "CREATE_VM", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.100", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{UserID: &userID, Action: "DELETE_VM", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.100", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	logs, err := suite.auditRepo.ListByAction(ctx, "CREATE_VM", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 4)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	count, err := suite.auditRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		auditLog := &models.AuditLog{UserID: &userID, Action: "ACTION", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.100", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	count, err = suite.auditRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_CountByUser() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	for i := 0; i < 5; i++ {
		auditLog := &models.AuditLog{UserID: &userID, Action: "ACTION", ResourceType: "vm", ResourceID: func() *string { s := uuid.New().String(); return &s }(), IPAddress: "192.168.1.100", Details: map[string]any{}}
		suite.DB.Create(auditLog)
	}

	count, err := suite.auditRepo.CountByUser(ctx, userID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}
