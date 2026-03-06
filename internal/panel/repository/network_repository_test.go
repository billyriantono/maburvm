package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type NetworkRepositoryTestSuite struct {
	BaseTestSuite
	networkRepo *NetworkRepository
}

func (suite *NetworkRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.networkRepo = NewNetworkRepository(suite.DB)
}

func TestNetworkRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(NetworkRepositoryTestSuite))
}

func (suite *NetworkRepositoryTestSuite) createTestVM() *models.VM {
	user := &models.User{Email: "network-test@example.com", PasswordHash: "hashedpassword", Role: models.RoleClient}
	suite.DB.Create(user)
	node := &models.Node{Name: "network-test-node", IPAddress: "192.168.100.1", Status: models.NodeStatusActive, Token: "network-test-token"}
	suite.DB.Create(node)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "network-test-vm", OSTemplateID: "00000000-0000-0000-0000-000000000001", Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
	suite.DB.Create(vm)
	return vm
}

func (suite *NetworkRepositoryTestSuite) TestNewNetworkRepository() {
	assert.NotNil(suite.T(), suite.networkRepo)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetByID() {
	ctx := context.Background()
	vm := suite.createTestVM()

	network := &models.Network{VMID: vm.ID, IPAddress: "10.0.0.10", BandwidthLimit: 1000}
	err := suite.DB.Create(network).Error
	assert.NoError(suite.T(), err)

	found, err := suite.networkRepo.GetByID(ctx, network.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetByVMID() {
	ctx := context.Background()
	vm := suite.createTestVM()

	network := &models.Network{VMID: vm.ID, IPAddress: "10.0.0.11", BandwidthLimit: 500}
	err := suite.DB.Create(network).Error
	assert.NoError(suite.T(), err)

	found, err := suite.networkRepo.GetByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_List() {
	ctx := context.Background()
	vm := suite.createTestVM()

	for i := 0; i < 5; i++ {
		network := &models.Network{VMID: vm.ID, IPAddress: "10.0.0." + string(rune('2'+i)), BandwidthLimit: int64(100 * (i + 1))}
		err := suite.DB.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	networks, err := suite.networkRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), networks, 5)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Create() {
	ctx := context.Background()
	vm := suite.createTestVM()

	network := &models.Network{VMID: vm.ID, IPAddress: "192.168.50.10", BandwidthLimit: 1000}
	err := suite.networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), network.ID)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Update() {
	ctx := context.Background()
	vm := suite.createTestVM()

	network := &models.Network{VMID: vm.ID, IPAddress: "10.0.2.1", BandwidthLimit: 500}
	err := suite.DB.Create(network).Error
	assert.NoError(suite.T(), err)

	network.BandwidthLimit = 1000
	err = suite.networkRepo.Update(ctx, network)
	assert.NoError(suite.T(), err)

	var found models.Network
	err = suite.DB.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(1000), found.BandwidthLimit)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Delete() {
	ctx := context.Background()
	vm := suite.createTestVM()

	network := &models.Network{VMID: vm.ID, IPAddress: "10.0.3.1", BandwidthLimit: 500}
	err := suite.DB.Create(network).Error
	assert.NoError(suite.T(), err)

	err = suite.networkRepo.Delete(ctx, network.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.Network{}).Where("id = ?", network.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Count() {
	ctx := context.Background()
	vm := suite.createTestVM()

	count, err := suite.networkRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		network := &models.Network{VMID: vm.ID, IPAddress: "10.0.5." + string(rune('1'+i)), BandwidthLimit: int64(100 * (i + 1))}
		err := suite.DB.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.networkRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}
