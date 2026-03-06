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

// VMRepositoryTestSuite tests VMRepository
type VMRepositoryTestSuite struct {
	suite.Suite
	db     *gorm.DB
	vmRepo *VMRepository
}

func (suite *VMRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.User{}, &models.Node{}, &models.VM{}, &models.OSTemplate{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.vmRepo = NewVMRepository(suite.db)
}

func (suite *VMRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM vms")
	suite.db.Exec("DELETE FROM users")
	suite.db.Exec("DELETE FROM nodes")
	suite.db.Exec("DELETE FROM os_templates")
}

func (suite *VMRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestVMRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(VMRepositoryTestSuite))
}

func (suite *VMRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{
		Email:        "vm-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test user: %v", err)
	}
	return user
}

func (suite *VMRepositoryTestSuite) createTestNode() *models.Node {
	node := &models.Node{
		Name:      "test-node",
		IPAddress: "192.168.1.1",
		Status:    models.NodeStatusActive,
		Token:     "test-token",
	}
	err := suite.db.Create(node).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test node: %v", err)
	}
	return node
}

func (suite *VMRepositoryTestSuite) createTestTemplate() *models.OSTemplate {
	template := &models.OSTemplate{
		Name:      "Ubuntu",
		Version:   "22.04",
		ImagePath: "/images/ubuntu.img",
		IsActive:  true,
	}
	err := suite.db.Create(template).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test template: %v", err)
	}
	return template
}

func (suite *VMRepositoryTestSuite) TestNewVMRepository() {
	assert.NotNil(suite.T(), suite.vmRepo)
	assert.NotNil(suite.T(), suite.vmRepo.base)
	assert.NotNil(suite.T(), suite.vmRepo.db)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "test-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.vmRepo.GetByID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByID_NotFound() {
	ctx := context.Background()

	_, err := suite.vmRepo.GetByID(ctx, "non-existent-id")
	assert.Error(suite.T(), err)
	assert.Equal(suite.T(), gorm.ErrRecordNotFound, err)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByIDWithUser() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "vm-with-user",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Get with user preloaded
	found, err := suite.vmRepo.GetByIDWithUser(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByIDWithNode() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "vm-with-node",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Get with node preloaded
	found, err := suite.vmRepo.GetByIDWithNode(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs
	for i := 0; i < 5; i++ {
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

	// List all
	vms, err := suite.vmRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 5)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_ListByUserID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs for user
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "user-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// List by user ID
	vms, err := suite.vmRepo.ListByUserID(ctx, user.ID.String(), 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 3)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_ListByNodeID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs on node
	for i := 0; i < 4; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "node-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// List by node ID
	vms, err := suite.vmRepo.ListByNodeID(ctx, node.ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 4)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_ListByStatus() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create running VMs
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "running-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Create stopped VMs
	for i := 0; i < 2; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "stopped-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusStopped,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// List by status
	runningVMs, err := suite.vmRepo.ListByStatus(ctx, models.VMStatusRunning, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), runningVMs, 3)

	stoppedVMs, err := suite.vmRepo.ListByStatus(ctx, models.VMStatusStopped, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), stoppedVMs, 2)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Create() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "new-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 4, RAM: 8192, Disk: 100},
		Status:       models.VMStatusStopped,
	}

	err := suite.vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), vm.ID)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Update() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "update-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusStopped,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Update
	vm.Status = models.VMStatusRunning
	err = suite.vmRepo.Update(ctx, vm)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.VMStatusRunning, found.Status)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Delete() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "delete-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusStopped,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.vmRepo.Delete(ctx, vm.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.VM{}).Where("id = ?", vm.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Initial count
	count, err := suite.vmRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create VMs
	for i := 0; i < 5; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "count-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.vmRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_CountByUserID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs for user
	for i := 0; i < 4; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "user-count-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Count by user ID
	count, err := suite.vmRepo.CountByUserID(ctx, user.ID.String())
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(4), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_CountByNodeID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs on node
	for i := 0; i < 3; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "node-count-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Count by node ID
	count, err := suite.vmRepo.CountByNodeID(ctx, node.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(3), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_UpdateStatus() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "status-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusStopped,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Update status
	err = suite.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusRunning)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.VMStatusRunning, found.Status)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_UpdateResources() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "resources-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusStopped,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Update resources
	newResources := models.Resources{CPU: 4, RAM: 8192, Disk: 100}
	err = suite.vmRepo.UpdateResources(ctx, vm.ID, newResources)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.VM
	err = suite.db.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 4, found.Resources.CPU)
	assert.Equal(suite.T(), 8192, found.Resources.RAM)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByHostname() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "hostname-test-vm",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err := suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Get by hostname
	found, err := suite.vmRepo.GetByHostname(ctx, "hostname-test-vm")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.ID, found.ID)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_HostnameExists() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Check non-existent hostname
	exists, err := suite.vmRepo.HostnameExists(ctx, "nonexistent-vm")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	// Create VM
	vm := &models.VM{
		UserID:       user.ID.String(),
		NodeID:       node.ID,
		Hostname:     "existing-hostname",
		OSTemplateID: template.ID,
		Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
		Status:       models.VMStatusRunning,
	}
	err = suite.db.Create(vm).Error
	assert.NoError(suite.T(), err)

	// Check existing hostname
	exists, err = suite.vmRepo.HostnameExists(ctx, "existing-hostname")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetIDs() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs
	for i := 0; i < 5; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "ids-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Get all IDs
	ids, err := suite.vmRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 5)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetIDsByUserID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	// Create VMs for user
	for i := 0; i < 4; i++ {
		vm := &models.VM{
			UserID:       user.ID.String(),
			NodeID:       node.ID,
			Hostname:     "user-ids-vm-" + string(rune('0'+i)),
			OSTemplateID: template.ID,
			Resources:    models.Resources{CPU: 2, RAM: 4096, Disk: 50},
			Status:       models.VMStatusRunning,
		}
		err := suite.db.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	// Get IDs by user
	ids, err := suite.vmRepo.GetIDsByUserID(ctx, user.ID.String())
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 4)
}
