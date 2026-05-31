package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"

	"github.com/maburvm/panel/internal/shared/models"
)

type FirewallRepositoryTestSuite struct {
	BaseTestSuite
	firewallRepo *FirewallRepository
}

func (suite *FirewallRepositoryTestSuite) SetupSuite() {
	suite.BaseTestSuite.SetupSuite()
	suite.firewallRepo = NewFirewallRepository(suite.DB)
}

func TestFirewallRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(FirewallRepositoryTestSuite))
}

func (suite *FirewallRepositoryTestSuite) createTestVM() *models.VM {
	user := &models.User{Email: "firewall-test@example.com", PasswordHash: "hashedpassword", Role: models.RoleClient}
	suite.DB.Create(user)
	node := &models.Node{Name: "firewall-test-node", IPAddress: "192.168.200.1", Status: models.NodeStatusActive, Token: "firewall-test-token"}
	suite.DB.Create(node)
	vm := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "firewall-test-vm", OSTemplateID: "00000000-0000-0000-0000-000000000001", Resources: models.Resources{CPU: 2, RAM: 4096, Disk: 50}, Status: models.VMStatusRunning}
	suite.DB.Create(vm)
	return vm
}

func (suite *FirewallRepositoryTestSuite) TestNewFirewallRepository() {
	assert.NotNil(suite.T(), suite.firewallRepo)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_GetByID() {
	ctx := context.Background()
	vm := suite.createTestVM()

	rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "80", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100}
	err := suite.DB.Create(rule).Error
	assert.NoError(suite.T(), err)

	found, err := suite.firewallRepo.GetByID(ctx, rule.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), rule.Protocol, found.Protocol)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_List() {
	ctx := context.Background()
	vm := suite.createTestVM()

	for i := 0; i < 5; i++ {
		rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: string(rune('8'+i)) + "0", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100 + i}
		err := suite.DB.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	rules, err := suite.firewallRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), rules, 5)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByVMID() {
	ctx := context.Background()
	vm := suite.createTestVM()

	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: string(rune('1'+i)) + "000", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100 + i}
		err := suite.DB.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	rules, err := suite.firewallRepo.ListByVMID(ctx, vm.ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), rules, 3)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByAction() {
	ctx := context.Background()
	vm := suite.createTestVM()

	for i := 0; i < 4; i++ {
		rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "80", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100 + i}
		err := suite.DB.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	for i := 0; i < 2; i++ {
		rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "22", Action: "deny", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 200 + i}
		err := suite.DB.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	allowRules, err := suite.firewallRepo.ListByAction(ctx, "allow", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), allowRules, 4)

	denyRules, err := suite.firewallRepo.ListByAction(ctx, "deny", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), denyRules, 2)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Create() {
	ctx := context.Background()
	vm := suite.createTestVM()

	rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "443", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100}
	err := suite.firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), rule.ID)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Update() {
	ctx := context.Background()
	vm := suite.createTestVM()

	rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "80", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100}
	err := suite.DB.Create(rule).Error
	assert.NoError(suite.T(), err)

	rule.Priority = 50
	err = suite.firewallRepo.Update(ctx, rule)
	assert.NoError(suite.T(), err)

	var found models.FirewallRule
	err = suite.DB.First(&found, "id = ?", rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 50, found.Priority)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Delete() {
	ctx := context.Background()
	vm := suite.createTestVM()

	rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "22", Action: "deny", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100}
	err := suite.DB.Create(rule).Error
	assert.NoError(suite.T(), err)

	err = suite.firewallRepo.Delete(ctx, rule.ID)
	assert.NoError(suite.T(), err)

	var count int64
	suite.DB.Unscoped().Model(&models.FirewallRule{}).Where("id = ?", rule.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Count() {
	ctx := context.Background()
	vm := suite.createTestVM()

	count, err := suite.firewallRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	for i := 0; i < 5; i++ {
		rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: string(rune('8'+i)) + "0", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100 + i}
		err := suite.DB.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.firewallRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_UpdatePriority() {
	ctx := context.Background()
	vm := suite.createTestVM()

	rule := &models.FirewallRule{VMID: vm.ID, Protocol: "tcp", PortRange: "80", Action: "allow", Direction: "inbound", SourceIP: "0.0.0.0/0", Priority: 100}
	err := suite.DB.Create(rule).Error
	assert.NoError(suite.T(), err)

	err = suite.firewallRepo.UpdatePriority(ctx, rule.ID, 200)
	assert.NoError(suite.T(), err)

	var found models.FirewallRule
	err = suite.DB.First(&found, "id = ?", rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 200, found.Priority)
}
