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

// FirewallRepositoryTestSuite tests FirewallRepository
type FirewallRepositoryTestSuite struct {
	suite.Suite
	db           *gorm.DB
	firewallRepo *FirewallRepository
}

func (suite *FirewallRepositoryTestSuite) SetupSuite() {
	var err error
	suite.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		suite.T().Fatalf("Failed to connect to test database: %v", err)
	}

	err = suite.db.AutoMigrate(&models.User{}, &models.Node{}, &models.VM{}, &models.FirewallRule{})
	if err != nil {
		suite.T().Fatalf("Failed to migrate: %v", err)
	}

	suite.firewallRepo = NewFirewallRepository(suite.db)
}

func (suite *FirewallRepositoryTestSuite) SetupTest() {
	suite.db.Exec("DELETE FROM firewall_rules")
	suite.db.Exec("DELETE FROM vms")
	suite.db.Exec("DELETE FROM users")
	suite.db.Exec("DELETE FROM nodes")
}

func (suite *FirewallRepositoryTestSuite) TearDownSuite() {
	sqlDB, err := suite.db.DB()
	if err == nil {
		sqlDB.Close()
	}
}

func TestFirewallRepositoryTestSuite(t *testing.T) {
	suite.Run(t, new(FirewallRepositoryTestSuite))
}

func (suite *FirewallRepositoryTestSuite) createTestUser() *models.User {
	user := &models.User{
		Email:        "firewall-test@example.com",
		PasswordHash: "hashedpassword",
		Role:         models.RoleClient,
	}
	err := suite.db.Create(user).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test user: %v", err)
	}
	return user
}

func (suite *FirewallRepositoryTestSuite) createTestNode() *models.Node {
	node := &models.Node{
		Name:      "firewall-test-node",
		IPAddress: "192.168.200.1",
		Status:    models.NodeStatusActive,
		Token:     "firewall-test-token",
	}
	err := suite.db.Create(node).Error
	if err != nil {
		suite.T().Fatalf("Failed to create test node: %v", err)
	}
	return node
}

func (suite *FirewallRepositoryTestSuite) createTestVM(userID, nodeID string) *models.VM {
	vm := &models.VM{
		UserID:       userID,
		NodeID:       nodeID,
		Hostname:     "firewall-test-vm",
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

func (suite *FirewallRepositoryTestSuite) TestNewFirewallRepository() {
	assert.NotNil(suite.T(), suite.firewallRepo)
	assert.NotNil(suite.T(), suite.firewallRepo.base)
	assert.NotNil(suite.T(), suite.firewallRepo.db)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_GetByID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create firewall rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Get by ID
	found, err := suite.firewallRepo.GetByID(ctx, rule.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), rule.Protocol, found.Protocol)
	assert.Equal(suite.T(), rule.PortRange, found.PortRange)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_List() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create firewall rules
	for i := 0; i < 5; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// List all
	rules, err := suite.firewallRepo.List(ctx, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), rules, 5)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create firewall rules for VM
	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('1'+i)) + "000",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// List by VM ID
	rules, err := suite.firewallRepo.ListByVMID(ctx, vm.ID, 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), rules, 3)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByVMIDAndDirection() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create inbound rules
	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Create outbound rules
	for i := 0; i < 2; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: "443",
			Action:    "allow",
			Direction: "outbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  200 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// List inbound
	inboundRules, err := suite.firewallRepo.ListByVMIDAndDirection(ctx, vm.ID, "inbound", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), inboundRules, 3)

	// List outbound
	outboundRules, err := suite.firewallRepo.ListByVMIDAndDirection(ctx, vm.ID, "outbound", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), outboundRules, 2)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByProtocol() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create TCP rules
	for i := 0; i < 3; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: "80",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Create UDP rules
	for i := 0; i < 2; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "udp",
			PortRange: "53",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  200 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// List TCP rules
	tcpRules, err := suite.firewallRepo.ListByProtocol(ctx, "tcp", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), tcpRules, 3)

	// List UDP rules
	udpRules, err := suite.firewallRepo.ListByProtocol(ctx, "udp", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), udpRules, 2)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_ListByAction() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create allow rules
	for i := 0; i < 4; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: "80",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Create deny rules
	for i := 0; i < 2; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: "22",
			Action:    "deny",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  200 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// List allow rules
	allowRules, err := suite.firewallRepo.ListByAction(ctx, "allow", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), allowRules, 4)

	// List deny rules
	denyRules, err := suite.firewallRepo.ListByAction(ctx, "deny", 0, 0)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), denyRules, 2)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Create() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "443",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}

	err := suite.firewallRepo.Create(ctx, rule)
	assert.NoError(suite.T(), err)
	assert.NotEmpty(suite.T(), rule.ID)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), rule.Protocol, found.Protocol)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Update() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Update
	rule.Priority = 50
	err = suite.firewallRepo.Update(ctx, rule)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 50, found.Priority)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Delete() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "22",
		Action:    "deny",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Delete
	err = suite.firewallRepo.Delete(ctx, rule.ID)
	assert.NoError(suite.T(), err)

	// Verify hard delete
	var count int64
	suite.db.Unscoped().Model(&models.FirewallRule{}).Where("id = ?", rule.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_DeleteByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rules for VM
	for i := 0; i < 4; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Delete by VM ID
	err := suite.firewallRepo.DeleteByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)

	// Verify deletion
	var count int64
	suite.db.Unscoped().Model(&models.FirewallRule{}).Where("vm_id = ?", vm.ID).Count(&count)
	assert.Equal(suite.T(), int64(0), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_Count() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Initial count
	count, err := suite.firewallRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(0), count)

	// Create rules
	for i := 0; i < 5; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	count, err = suite.firewallRepo.Count(ctx)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(5), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_CountByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rules for VM
	for i := 0; i < 6; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i%3)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Count by VM ID
	count, err := suite.firewallRepo.CountByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), int64(6), count)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_UpdatePriority() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Update priority
	err = suite.firewallRepo.UpdatePriority(ctx, rule.ID, 200)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), 200, found.Priority)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_UpdateAction() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "22",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Update action
	err = suite.firewallRepo.UpdateAction(ctx, rule.ID, "deny")
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), "deny", found.Action)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_UpdateSourceIP() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "22",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Update source IP
	newIP := "192.168.1.0/24"
	err = suite.firewallRepo.UpdateSourceIP(ctx, rule.ID, newIP)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newIP, found.SourceIP)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_UpdatePortRange() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rule
	rule := &models.FirewallRule{
		VMID:      vm.ID,
		Protocol:  "tcp",
		PortRange: "80",
		Action:    "allow",
		Direction: "inbound",
		SourceIP:  "0.0.0.0/0",
		Priority:  100,
	}
	err := suite.db.Create(rule).Error
	assert.NoError(suite.T(), err)

	// Update port range
	newPortRange := "443"
	err = suite.firewallRepo.UpdatePortRange(ctx, rule.ID, newPortRange)
	assert.NoError(suite.T(), err)

	// Verify
	var found models.FirewallRule
	err = suite.db.First(&found, rule.ID).Error
	assert.NoError(suite.T(), err)
	assert.Equal(suite.T(), newPortRange, found.PortRange)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_GetIDs() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rules
	for i := 0; i < 5; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Get all IDs
	ids, err := suite.firewallRepo.GetIDs(ctx)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 5)
}

func (suite *FirewallRepositoryTestSuite) TestFirewallRepository_GetIDsByVMID() {
	ctx := context.Background()
	user := suite.createTestUser()
	node := suite.createTestNode()
	vm := suite.createTestVM(user.ID.String(), node.ID)

	// Create rules for VM
	for i := 0; i < 4; i++ {
		rule := &models.FirewallRule{
			VMID:      vm.ID,
			Protocol:  "tcp",
			PortRange: string(rune('8'+i)) + "0",
			Action:    "allow",
			Direction: "inbound",
			SourceIP:  "0.0.0.0/0",
			Priority:  100 + i,
		}
		err := suite.db.Create(rule).Error
		assert.NoError(suite.T(), err)
	}

	// Get IDs by VM ID
	ids, err := suite.firewallRepo.GetIDsByVMID(ctx, vm.ID)
	assert.NoError(suite.T(), err)
	assert.Len(suite.T(), ids, 4)
}
