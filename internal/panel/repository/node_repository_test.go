package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// NodeRepositoryTestSuite tests NodeRepository
type NodeRepositoryTestSuite struct {
	suite.Suite
	db       *gorm.DB
	nodeRepo *NodeRepository
}

func (suite *NodeRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.Node{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.nodeRepo = NewNodeRepository(suite.db)
}

func (suite *NodeRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM nodes")
}

func (suite *NodeRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestNodeRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(NodeRepositoryTestSuite))
}

func (suite *NodeRepositoryTestSuite) TestNewNodeRepository() {
	assert.NotNil(suite.T(), suite.nodeRepo)
	assert.NotNil(suite.T(), suite.nodeRepo.base)
	assert.NotNil(suite.T(), suite.nodeRepo.db)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByID() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "test-node",
		IPAddress: "192.168.1.1",
		Status:    models.NodeStatusActive,
		Token:     "test-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.nodeRepo.GetByID(ctx, node.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.Name, found.Name)
	assert.Equal(suite.T(), node.IPAddress, found.IPAddress)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByID_NotFound() {
	ctx := context.Background()

	_, err := suite.nodeRepo.GetByID(ctx, "non-existent-id")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByToken() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "token-node",
		IPAddress: "192.168.1.2",
		Status:    models.NodeStatusActive,
		Token:     "secret-token-123",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Get by token
	found, err := suite.nodeRepo.GetByToken(ctx, "secret-token-123")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.ID, found.ID)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByIPAddress() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "ip-node",
		IPAddress: "10.0.0.1",
		Status:    models.NodeStatusActive,
		Token:     "token-1",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Get by IP address
	found, err := suite.nodeRepo.GetByIPAddress(ctx, "10.0.0.1")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.Name, found.Name)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_List() {
	ctx := context.Background()

	// Create nodes
	for i := 0; i < 5; i++ {
		node := &models.Node{
			Name:      "node-" + string(rune('0'+i)),
			IPAddress: "192.168.1." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	nodes, err := suite.nodeRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), nodes, 5)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_ListByStatus() {
	ctx := context.Background()

	// Create active nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "active-node-" + string(rune('0'+i)),
			IPAddress: "192.168.2." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "active-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// Create offline nodes
	for i := 0; i < 2; i++ {
		node := &models.Node{
			Name:      "offline-node-" + string(rune('0'+i)),
			IPAddress: "192.168.3." + string(rune('1'+i)),
			Status:    models.NodeStatusOffline,
			Token:     "offline-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// List active
	activeNodes, err := suite.nodeRepo.ListByStatus(ctx, models.NodeStatusActive, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeNodes, 3)

	// List offline
	offlineNodes, err := suite.nodeRepo.ListByStatus(ctx, models.NodeStatusOffline, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), offlineNodes, 2)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_ListActive() {
	ctx := context.Background()

	// Create mixed status nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "all-active-" + string(rune('0'+i)),
			IPAddress: "192.168.4." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "all-active-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		node := &models.Node{
			Name:      "all-offline-" + string(rune('0'+i)),
			IPAddress: "192.168.5." + string(rune('1'+i)),
			Status:    models.NodeStatusOffline,
			Token:     "all-offline-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// List active
	activeNodes, err := suite.nodeRepo.ListActive(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeNodes, 3)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Create() {
	ctx := context.Background()

	node := &models.Node{
		Name:      "new-node",
		IPAddress: "10.10.10.10",
		Status:    models.NodeStatusOffline,
		Token:     "new-token",
	}

	err := suite.nodeRepo.Create(ctx, node)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), node.ID)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.Name, found.Name)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Update() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "update-node",
		IPAddress: "10.0.0.2",
		Status:    models.NodeStatusOffline,
		Token:     "update-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Update
	node.Status = models.NodeStatusActive
	err = suite.nodeRepo.Update(ctx, node)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.NodeStatusActive, found.Status)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Delete() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "delete-node",
		IPAddress: "10.0.0.3",
		Status:    models.NodeStatusOffline,
		Token:     "delete-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.nodeRepo.Delete(ctx, node.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.Node{}).Where("id = ?", node.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Count() {
	ctx := context.Background()

	// Initial count
	count, err := suite.nodeRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create nodes
	for i := 0; i < 5; i++ {
		node := &models.Node{
			Name:      "count-node-" + string(rune('0'+i)),
			IPAddress: "192.168.6." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "count-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.nodeRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_CountByStatus() {
	ctx := context.Background()

	// Create mixed status nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "status-active-" + string(rune('0'+i)),
			IPAddress: "192.168.7." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "status-active-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		node := &models.Node{
			Name:      "status-offline-" + string(rune('0'+i)),
			IPAddress: "192.168.8." + string(rune('1'+i)),
			Status:    models.NodeStatusOffline,
			Token:     "status-offline-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// Count by status
	activeCount, err := suite.nodeRepo.CountByStatus(ctx, models.NodeStatusActive)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), activeCount)

	offlineCount, err := suite.nodeRepo.CountByStatus(ctx, models.NodeStatusOffline)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(2), offlineCount)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_UpdateStatus() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "status-update-node",
		IPAddress: "10.0.0.4",
		Status:    models.NodeStatusOffline,
		Token:     "status-update-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Update status
	err = suite.nodeRepo.UpdateStatus(ctx, node.ID, models.NodeStatusActive)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.NodeStatusActive, found.Status)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_UpdateToken() {
	ctx := context.Background()

	// Create node
	node := &models.Node{
		Name:      "token-update-node",
		IPAddress: "10.0.0.5",
		Status:    models.NodeStatusActive,
		Token:     "old-token",
	}
	err := suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Update token
	newToken := "new-secret-token"
	err = suite.nodeRepo.UpdateToken(ctx, node.ID, newToken)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Node
	err = suite.db.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newToken, found.Token)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_NameExists() {
	ctx := context.Background()

	// Check non-existent name
	exists, err := suite.nodeRepo.NameExists(ctx, "nonexistent")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create node
	node := &models.Node{
		Name:      "existing-name",
		IPAddress: "10.0.0.6",
		Status:    models.NodeStatusActive,
		Token:     "name-token",
	}
	err = suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Check existing name
	exists, err = suite.nodeRepo.NameExists(ctx, "existing-name")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_IPAddressExists() {
	ctx := context.Background()

	// Check non-existent IP
	exists, err := suite.nodeRepo.IPAddressExists(ctx, "10.99.99.99")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create node
	node := &models.Node{
		Name:      "ip-check-node",
		IPAddress: "10.0.0.7",
		Status:    models.NodeStatusActive,
		Token:     "ip-check-token",
	}
	err = suite.db.Create(node).Error
	assert.NoError(suite.T(), err)

	// Check existing IP
	exists, err = suite.nodeRepo.IPAddressExists(ctx, "10.0.0.7")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetIDs() {
	ctx := context.Background()

	// Create nodes
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "ids-node-" + string(rune('0'+i)),
			IPAddress: "192.168.9." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "ids-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
		ids = append(ids, node.ID)
	}

	// Get all IDs
	retrievedIDs, err := suite.nodeRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), retrievedIDs, 3)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetIDsByStatus() {
	ctx := context.Background()

	// Create active nodes
	for i := 0; i < 3; i++ {
		node := &models.Node{
			Name:      "ids-active-" + string(rune('0'+i)),
			IPAddress: "192.168.10." + string(rune('1'+i)),
			Status:    models.NodeStatusActive,
			Token:     "ids-active-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// Create offline nodes
	for i := 0; i < 2; i++ {
		node := &models.Node{
			Name:      "ids-offline-" + string(rune('0'+i)),
			IPAddress: "192.168.11." + string(rune('1'+i)),
			Status:    models.NodeStatusOffline,
			Token:     "ids-offline-token-" + string(rune('0'+i)),
		}
		err := suite.db.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	// Get IDs by status
	activeIDs, err := suite.nodeRepo.GetIDsByStatus(ctx, models.NodeStatusActive)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeIDs, 3)

	offlineIDs, err := suite.nodeRepo.GetIDsByStatus(ctx, models.NodeStatusOffline)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), offlineIDs, 2)
}
