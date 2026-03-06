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

// AuditRepositoryTestSuite tests AuditRepository
type AuditRepositoryTestSuite struct {
	suite.Suite
	db        *gorm.DB
	auditRepo *AuditRepository
}

func (suite *AuditRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.User{}, &models.AuditLog{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.auditRepo = NewAuditRepository(suite.db)
}

func (suite *AuditRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM audit_logs")
	suite.db.Exec("DELETE FROM users")
}

func (suite *AuditRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestAuditRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(AuditRepositoryTestSuite))
}

func (suite *AuditRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{
		Email:        "audit-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test user: %v", err)
	}
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

	// Verify
	var found models.AuditLog
	err = suite.db.First(&found, auditLog.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), auditLog.Action, found.Action)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Create audit log
	auditLog := &models.AuditLog{
		UserID:       &userID,
		Action:       "DELETE_VM",
		ResourceType: "vm",
		ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
		IPAddress:    "192.168.1.100",
		Details:      map[string]any{"vm_id": "vm-123"},
	}
	err := suite.db.Create(auditLog).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.auditRepo.GetByID(ctx, auditLog.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), auditLog.Action, found.Action)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Create audit logs
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
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	logs, err := suite.auditRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 5)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_ListByUser() {
	ctx := context.Background()

	// Create users
	user1 := suite.createTestUser()
	user2 := &models.User{
		Email:        "user2@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user2).Error
	assert.NoError(suite.T(), err)

	user1ID := user1.ID.String()
	user2ID := user2.ID.String()

	// Create logs for user1
	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{
			UserID:       &user1ID,
			Action:       "ACTION_" + string(rune('0'+i)),
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Create logs for user2
	for i := 0; i < 2; i++ {
		auditLog := &models.AuditLog{
			UserID:       &user2ID,
			Action:       "ACTION_" + string(rune('0'+i)),
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.101",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// List by user1
	logs, err := suite.auditRepo.ListByUser(ctx, user1ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 3)

	// List by user2
	logs, err = suite.auditRepo.ListByUser(ctx, user2ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 2)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_ListByAction() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Create logs with different actions
	for i := 0; i < 4; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "CREATE_VM",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "DELETE_VM",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// List by CREATE_VM action
	logs, err := suite.auditRepo.ListByAction(ctx, "CREATE_VM", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 4)

	// List by DELETE_VM action
	logs, err = suite.auditRepo.ListByAction(ctx, "DELETE_VM", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 3)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_ListByResource() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()
	resourceID := uuid.New().String()

	// Create logs for specific resource
	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "ACTION_" + string(rune('0'+i)),
			ResourceType: "vm",
			ResourceID:   &resourceID,
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Create logs for other resources
	for i := 0; i < 2; i++ {
		otherID := uuid.New().String()
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "OTHER_ACTION",
			ResourceType: "vm",
			ResourceID:   &otherID,
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// List by resource
	logs, err := suite.auditRepo.ListByResource(ctx, "vm", resourceID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 3)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Initial count
	count, err := suite.auditRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create logs
	for i := 0; i < 5; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "ACTION_" + string(rune('0'+i)),
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.auditRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_CountByUser() {
	ctx := context.Background()

	// Create users
	user1 := suite.createTestUser()
	user2 := &models.User{
		Email:        "user2-count@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user2).Error
	assert.NoError(suite.T(), err)

	user1ID := user1.ID.String()
	user2ID := user2.ID.String()

	// Create logs for user1
	for i := 0; i < 5; i++ {
		auditLog := &models.AuditLog{
			UserID:       &user1ID,
			Action:       "ACTION",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Create logs for user2
	for i := 0; i < 3; i++ {
		auditLog := &models.AuditLog{
			UserID:       &user2ID,
			Action:       "ACTION",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.101",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Count by user
	count1, err := suite.auditRepo.CountByUser(ctx, user1ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count1)

	count2, err := suite.auditRepo.CountByUser(ctx, user2ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), count2)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_CountByAction() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Create CREATE_VM logs
	for i := 0; i < 6; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "CREATE_VM",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Create DELETE_VM logs
	for i := 0; i < 4; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "DELETE_VM",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Count by action
	createCount, err := suite.auditRepo.CountByAction(ctx, "CREATE_VM")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(6), createCount)

	deleteCount, err := suite.auditRepo.CountByAction(ctx, "DELETE_VM")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(4), deleteCount)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_Create_WithSnapshots() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()
	resourceID := uuid.New().String()

	beforeSnapshot := map[string]any{
		"name":   "old-vm",
		"cpu":    2,
		"ram":    4096,
		"status": "running",
	}

	afterSnapshot := map[string]any{
		"name":   "new-vm",
		"cpu":    4,
		"ram":    8192,
		"status": "running",
	}

	auditLog := &models.AuditLog{
		UserID:         &userID,
		Action:         "UPDATE_VM",
		ResourceType:   "vm",
		ResourceID:     &resourceID,
		IPAddress:      "192.168.1.100",
		UserAgent:      "Mozilla/5.0",
		Details:        map[string]any{"changes": []string{"cpu", "ram", "name"}},
		BeforeSnapshot: &beforeSnapshot,
		AfterSnapshot:  &afterSnapshot,
	}

	err := suite.auditRepo.Create(ctx, auditLog)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), auditLog.ID)

	// Verify
	var found models.AuditLog
	err = suite.db.First(&found, auditLog.ID).Error
	assert.NoError(suite.T(), err)
	assert.NotNil(suite.T(), found.BeforeSnapshot)
	assert.NotNil(suite.T(), found.AfterSnapshot)
	assert.Equal(suite.T(), 2, (*found.BeforeSnapshot)["cpu"])
	assert.Equal(suite.T(), 4, (*found.AfterSnapshot)["cpu"])
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_Create_SystemAction() {
	ctx := context.Background()

	// Create audit log without user (system action)
	auditLog := &models.AuditLog{
		Action:       "SYSTEM_BACKUP",
		ResourceType: "backup",
		ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
		IPAddress:    "127.0.0.1",
		UserAgent:    "System/1.0",
		Details:      map[string]any{"backup_type": "automatic"},
	}

	err := suite.auditRepo.Create(ctx, auditLog)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), auditLog.ID)
	assert.Nil(suite.T(), auditLog.UserID)

	// Verify
	var found models.AuditLog
	err = suite.db.First(&found, auditLog.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "SYSTEM_BACKUP", found.Action)
	assert.Nil(suite.T(), found.UserID)
}

func (suite *AuditRepositoryTestSuite) TestAuditRepository_List_Pagination() {
	ctx := context.Background()
	user := suite.createTestUser()
	userID := user.ID.String()

	// Create 10 logs
	for i := 0; i < 10; i++ {
		auditLog := &models.AuditLog{
			UserID:       &userID,
			Action:       "ACTION",
			ResourceType: "vm",
			ResourceID:   func() *string { s := uuid.New().String(); return &s }(),
			IPAddress:    "192.168.1.100",
			Details:      map[string]any{"index": i},
		}
		err := suite.db.Create(auditLog).Error
		assert.NoError(suite.T(), err)
	}

	// Test pagination
	logs, err := suite.auditRepo.List(ctx, 3, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 3)

	logs, err = suite.auditRepo.List(ctx, 0, 5)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), logs, 5)
}
