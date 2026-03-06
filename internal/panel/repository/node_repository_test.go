package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type NodeRepositoryTestSuite struct {
	BaseTestSuite
	nodeRepo *NodeRepository
}

func (suite *NodeRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.nodeRepo = NewNodeRepository(suite.DB)
}

func TestNodeRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(NodeRepositoryTestSuite))
}

func (suite *NodeRepositoryTestSuite) TestNewNodeRepository() {
	assert.NotNil(suite.T(), suite.nodeRepo)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByID() {
	ctx := context.Background()

	node := &models.Node{Name: "test-node", IPAddress: "192.168.1.1", Status: models.NodeStatusActive, Token: "test-token"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	found, err := suite.nodeRepo.GetByID(ctx, node.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.Name, found.Name)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByToken() {
	ctx := context.Background()

	node := &models.Node{Name: "token-node", IPAddress: "192.168.1.2", Status: models.NodeStatusActive, Token: "secret-token-123"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	found, err := suite.nodeRepo.GetByToken(ctx, "secret-token-123")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.ID, found.ID)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetByIPAddress() {
	ctx := context.Background()

	node := &models.Node{Name: "ip-node", IPAddress: "10.0.0.1", Status: models.NodeStatusActive, Token: "token-1"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	found, err := suite.nodeRepo.GetByIPAddress(ctx, "10.0.0.1")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), node.Name, found.Name)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_List() {
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		node := &models.Node{Name: "node-" + string(rune('0'+i)), IPAddress: "192.168.1." + string(rune('1'+i)), Status: models.NodeStatusActive, Token: "token-" + string(rune('0'+i))}
		err := suite.DB.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	nodes, err := suite.nodeRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), nodes, 5)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_ListByStatus() {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		node := &models.Node{Name: "active-node-" + string(rune('0'+i)), IPAddress: "192.168.2." + string(rune('1'+i)), Status: models.NodeStatusActive, Token: "active-token-" + string(rune('0'+i))}
		err := suite.DB.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		node := &models.Node{Name: "offline-node-" + string(rune('0'+i)), IPAddress: "192.168.3." + string(rune('1'+i)), Status: models.NodeStatusOffline, Token: "offline-token-" + string(rune('0'+i))}
		err := suite.DB.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	activeNodes, err := suite.nodeRepo.ListByStatus(ctx, models.NodeStatusActive, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), activeNodes, 3)

	offlineNodes, err := suite.nodeRepo.ListByStatus(ctx, models.NodeStatusOffline, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), offlineNodes, 2)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Create() {
	ctx := context.Background()

	node := &models.Node{Name: "new-node", IPAddress: "10.10.10.10", Status: models.NodeStatusOffline, Token: "new-token"}
	err := suite.nodeRepo.Create(ctx, node)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), node.ID)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Update() {
	ctx := context.Background()

	node := &models.Node{Name: "update-node", IPAddress: "10.0.0.2", Status: models.NodeStatusOffline, Token: "update-token"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	node.Status = models.NodeStatusActive
	err = suite.nodeRepo.Update(ctx, node)
	assert.NoError(suite.T(), err)

	var found models.Node
	err = suite.DB.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.NodeStatusActive, found.Status)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Delete() {
	ctx := context.Background()

	node := &models.Node{Name: "delete-node", IPAddress: "10.0.0.3", Status: models.NodeStatusOffline, Token: "delete-token"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	err = suite.nodeRepo.Delete(ctx, node.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.Node{}).Where("id = ?", node.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_Count() {
	ctx := context.Background()

	count, err := suite.nodeRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		node := &models.Node{Name: "count-node-" + string(rune('0'+i)), IPAddress: "192.168.6." + string(rune('1'+i)), Status: models.NodeStatusActive, Token: "count-token-" + string(rune('0'+i))}
		err := suite.DB.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.nodeRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_UpdateStatus() {
	ctx := context.Background()

	node := &models.Node{Name: "status-update-node", IPAddress: "10.0.0.4", Status: models.NodeStatusOffline, Token: "status-update-token"}
	err := suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	err = suite.nodeRepo.UpdateStatus(ctx, node.ID, models.NodeStatusActive)
	assert.NoError(suite.T(), err)

	var found models.Node
	err = suite.DB.First(&found, node.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.NodeStatusActive, found.Status)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_NameExists() {
	ctx := context.Background()

	exists, err := suite.nodeRepo.NameExists(ctx, "nonexistent")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	node := &models.Node{Name: "existing-name", IPAddress: "10.0.0.6", Status: models.NodeStatusActive, Token: "name-token"}
	err = suite.DB.Create(node).Error
	assert.NoError(suite.T(), err)

	exists, err = suite.nodeRepo.NameExists(ctx, "existing-name")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *NodeRepositoryTestSuite) TestNodeRepository_GetIDs() {
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		node := &models.Node{Name: "ids-node-" + string(rune('0'+i)), IPAddress: "192.168.9." + string(rune('1'+i)), Status: models.NodeStatusActive, Token: "ids-token-" + string(rune('0'+i))}
		err := suite.DB.Create(node).Error
		assert.NoError(suite.T(), err)
	}

	ids, err := suite.nodeRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 3)
}
