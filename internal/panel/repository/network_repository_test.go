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

// NetworkRepositoryTestSuite tests NetworkRepository
type NetworkRepositoryTestSuite struct {
	suite.Suite
	db          *gorm.DB
	networkRepo *NetworkRepository
}

func (suite *NetworkRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.User{}, &models.Node{}, &models.VM{}, &models.Network{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.networkRepo = NewNetworkRepository(suite.db)
}

func (suite *NetworkRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM networks")
	suite.db.Exec("DELETE FROM vms")
	suite.db.Exec("DELETE FROM users")
	suite.db.Exec("DELETE FROM nodes")
}

func (suite *NetworkRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestNetworkRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(NetworkRepositoryTestSuite))
}

func (suite *NetworkRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{
		Email:        "network-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test user: %v", err)
	}
	return user
}

func (suite *NetworkRepositoryTestSuite) createTestNode() *models.Node {
	node := &models.Node{
		Name:      "network-test-node",
		IPAddress: "192.168.100.1",
		Status:    models.NodeStatusActive,
		Token:     "network-test-token",
	}
	err := suite.db.Create(node).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test node: %v", err)
	}
	return node
}

func (suite *NetworkRepositoryTestSuite) createTestVM(userID, nodeID string) *models.VM {
	vm := &models.VM{
		UserID:       userID,
		NodeID:       nodeID,
		Hostname:     "network-test-vm",
		OSTemplateID: "00000000-0000-0000-0000-000000000001",
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err := suite.db.Create(vm).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test VM: %v", err)
	}
	return vm
}

func (suite *NetworkRepositoryTestSuite) TestNewNetworkRepository() {
	assert.NotNil(suite.T(), suite.networkRepo)
	assert.NotNil(suite.T(), suite.networkRepo.base)
	assert.NotNil(suite.T(), suite.networkRepo.db)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.0.10",
		BandwidthLimit: 1000,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.networkRepo.GetByID(ctx, network.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.0.11",
		BandwidthLimit: 500,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Get by VM ID
	found, err := suite.networkRepo.GetByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetByIPAddress() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.0.12",
		BandwidthLimit: 1000,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Get by IP address
	found, err := suite.networkRepo.GetByIPAddress(ctx, "10.0.0.12")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.ID, found.ID)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks
	for i := 0; i < 5; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.0." + string(rune('2'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	networks, err := suite.networkRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), networks, 5)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_ListByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks for VM
	for i := 0; i < 3; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.1." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// List by VM ID
	networks, err := suite.networkRepo.ListByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), networks, 3)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Create() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "192.168.50.10",
		BandwidthLimit: 1000,
		VLANID:         func() *int { v := 100; return &v }(),
	}

	err := suite.networkRepo.Create(ctx, network)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), network.ID)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), network.IPAddress, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Update() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.2.1",
		BandwidthLimit: 500,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Update
	network.BandwidthLimit = 1000
	err = suite.networkRepo.Update(ctx, network)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(1000), found.BandwidthLimit)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Delete() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.3.1",
		BandwidthLimit: 500,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.networkRepo.Delete(ctx, network.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.Network{}).Where("id = ?", network.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_DeleteByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks for VM
	for i := 0; i < 3; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.4." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// Delete by VM ID
	err := suite.networkRepo.DeleteByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)

	// Verify deletion
	var count int64
	suite.db.Unscoped().Model(&models.Network{}).Where("vm_id = ?", vm.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Initial count
	count, err := suite.networkRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create networks
	for i := 0; i < 5; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.5." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.networkRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_CountByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks for VM
	for i := 0; i < 4; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.6." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// Count by VM ID
	count, err := suite.networkRepo.CountByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(4), count)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_UpdateBandwidthLimit() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.7.1",
		BandwidthLimit: 500,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Update bandwidth limit
	err = suite.networkRepo.UpdateBandwidthLimit(ctx, network.ID, 2000)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(2000), found.BandwidthLimit)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_UpdateIPAddress() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.8.1",
		BandwidthLimit: 500,
	}
	err := suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Update IP address
	newIP := "192.168.100.50"
	err = suite.networkRepo.UpdateIPAddress(ctx, network.ID, newIP)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.Network
	err = suite.db.First(&found, network.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newIP, found.IPAddress)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_IPAddressExists() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Check non-existent IP
	exists, err := suite.networkRepo.IPAddressExists(ctx, "10.99.99.99")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create network
	network := &models.Network{
		VMID:           vm.ID,
		IPAddress:      "10.0.9.1",
		BandwidthLimit: 500,
	}
	err = suite.db.Create(network).Error
	assert.NoError(suite.T(), err)

	// Check existing IP
	exists, err = suite.networkRepo.IPAddressExists(ctx, "10.0.9.1")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetIDs() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks
	for i := 0; i < 5; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.10." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// Get all IDs
	ids, err := suite.networkRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 5)
}

func (suite *NetworkRepositoryTestSuite) TestNetworkRepository_GetIDsByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create networks for VM
	for i := 0; i < 4; i++ {
		network := &models.Network{
			VMID:           vm.ID,
			IPAddress:      "10.0.11." + string(rune('1'+i)),
			BandwidthLimit: int64(100 * (i + 1)),
		}
		err := suite.db.Create(network).Error
		assert.NoError(suite.T(), err)
	}

	// Get IDs by VM ID
	ids, err := suite.networkRepo.GetIDsByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 4)
}
