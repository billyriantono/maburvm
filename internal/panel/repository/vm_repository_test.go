package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type VMRepositoryTestSuite struct {
	BaseTestSuite
	vmRepo *VMRepository
}

func (suite *VMRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.vmRepo = NewVMRepository(suite.DB)
}

func TestVMRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(VMRepositoryTestSuite))
}

func (suite *VMRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{Email: "vm-test@example.com", PasswordHash: "hashedpassword", Role: models.RoleClient}
	err := suite.DB.Create(user).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test user: %v", err)
	}
	return user
}

func (suite *VMRepositoryTestSuite) createTestNode() *models.Node {
	node := &models.Node{Name: "test-node", IPAddress: "192.168.1.1", Status: models.NodeStatusActive, Token: "test-token"}
	err := suite.DB.Create(node).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test node: %v", err)
	}
	return node
}

func (suite *VMRepositoryTestSuite) createTestTemplate() *models.OSTemplate {
	template := &models.OSTemplate{Name: "Ubuntu", Version: "22.04", ImagePath: "/images/ubuntu.img", IsActive: true}
	err := suite.DB.Create(template).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test template: %v", err)
	}
	return template
}

func (suite *VMRepositoryTestSuite) TestNewVMRepository() {
	assert.NotNil(suite.T(), suite.vmRepo)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "test-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
	err := suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	found, err := suite.vmRepo.GetByID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.Hostname, found.Hostname)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	for i := 0; i < 5; i++ {
		vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "vm-" + string(rune('0'+i)), OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
		err := suite.DB.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	vms, err := suite.vmRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 5)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_ListByUserID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	for i := 0; i < 3; i++ {
		vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "user-vm-" + string(rune('0'+i)), OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
		err := suite.DB.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	vms, err := suite.vmRepo.ListByUserID(ctx, user.ID.String(), 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), vms, 3)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_ListByStatus() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	for i := 0; i < 3; i++ {
		vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "running-vm-" + string(rune('0'+i)), OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
		err := suite.DB.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "stopped-vm-" + string(rune('0'+i)), OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusStopped}
		err := suite.DB.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

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

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "new-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 4, RAM: 8192, Disk: 100}, Status: models.VMStatusStopped}
	err := suite.vmRepo.Create(ctx, vm)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), vm.ID)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Update() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "update-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusStopped}
	err := suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	vm.Status = models.VMStatusRunning
	err = suite.vmRepo.Update(ctx, vm)
	assert.NoError(suite.T(), err)

	var found models.VM
	err = suite.DB.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.VMStatusRunning, found.Status)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Delete() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "delete-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusStopped}
	err := suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	err = suite.vmRepo.Delete(ctx, vm.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.VM{}).Where("id = ?", vm.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	count, err := suite.vmRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "count-vm-" + string(rune('0'+i)), OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
		err := suite.DB.Create(vm).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.vmRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_UpdateStatus() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "status-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusStopped}
	err := suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	err = suite.vmRepo.UpdateStatus(ctx, vm.ID, models.VMStatusRunning)
	assert.NoError(suite.T(), err)

	var found models.VM
	err = suite.DB.First(&found, vm.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), models.VMStatusRunning, found.Status)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_GetByHostname() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "hostname-test-vm", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
	err := suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	found, err := suite.vmRepo.GetByHostname(ctx, "hostname-test-vm")
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), vm.ID, found.ID)
}

func (suite *VMRepositoryTestSuite) TestVMRepository_HostnameExists() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	template := suite.createTestTemplate()

	exists, err := suite.vmRepo.HostnameExists(ctx, "nonexistent-vm")
	assert.NoError(suite.T(), err)
	assert.False(suite.T(), exists)

	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "existing-hostname", OSTemplateID: template.ID, Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
	err = suite.DB.Create(vm).Error
	assert.NoError(suite.T(), err)

	exists, err = suite.vmRepo.HostnameExists(ctx, "existing-hostname")
	assert.NoError(suite.T(), err)
	assert.True(suite.T(), exists)
}
