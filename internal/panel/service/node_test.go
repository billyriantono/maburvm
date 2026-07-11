package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/suite"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// NodeServiceTestSuite tests the NodeService
type NodeServiceTestSuite struct {
	suite.Suite
	db      *gorm.DB
	repo    *repository.NodeRepository
	service *NodeService
	ctx     context.Context
}

func (s *NodeServiceTestSuite) SetupSuite() {
	var err error
	s.db, err = gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	s.Require().NoError(err)

	// Create a SQLite-compatible nodes table. The production model uses
	// PostgreSQL-specific types/defaults (uuid, inet, node_status,
	// gen_random_uuid()) that SQLite cannot auto-migrate.
	err = s.db.Exec(`CREATE TABLE IF NOT EXISTS nodes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		ip_address TEXT NOT NULL,
		status TEXT DEFAULT 'offline',
		token TEXT UNIQUE NOT NULL,
		cert_fingerprint TEXT NOT NULL DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`).Error
	s.Require().NoError(err)

	s.repo = repository.NewNodeRepository(s.db)
	s.service = NewNodeService(s.repo)
	s.ctx = context.Background()
}

func (s *NodeServiceTestSuite) TearDownSuite() {
	sqlDB, _ := s.db.DB()
	sqlDB.Close()
}

func (s *NodeServiceTestSuite) SetupTest() {
	// Clear nodes table before each test
	s.db.Exec("DELETE FROM nodes")
}

func (s *NodeServiceTestSuite) TestCreateNode() {
	req := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	resp, err := s.service.CreateNode(s.ctx, req)
	s.NoError(err)
	s.NotNil(resp)
	s.NotEmpty(resp.Node.ID)
	s.Equal("test-node", resp.Node.Name)
	s.Equal("192.168.1.100", resp.Node.IPAddress)
	s.Equal(models.NodeStatusOffline, resp.Node.Status)
	s.NotEmpty(resp.Token)
	s.Len(resp.Token, 64) // 32 bytes hex encoded = 64 chars
}

func (s *NodeServiceTestSuite) TestCreateNode_DuplicateName() {
	req := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	_, err := s.service.CreateNode(s.ctx, req)
	s.NoError(err)

	// Try to create with same name but different IP
	req2 := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.101",
	}

	_, err = s.service.CreateNode(s.ctx, req2)
	s.Error(err)
	s.ErrorIs(err, ErrNodeAlreadyExists)
}

func (s *NodeServiceTestSuite) TestCreateNode_DuplicateIP() {
	req := &CreateNodeRequest{
		Name:      "test-node-1",
		IPAddress: "192.168.1.100",
	}

	_, err := s.service.CreateNode(s.ctx, req)
	s.NoError(err)

	// Try to create with same IP but different name
	req2 := &CreateNodeRequest{
		Name:      "test-node-2",
		IPAddress: "192.168.1.100",
	}

	_, err = s.service.CreateNode(s.ctx, req2)
	s.Error(err)
	s.ErrorIs(err, ErrNodeAlreadyExists)
}

func (s *NodeServiceTestSuite) TestCreateNode_InvalidIP() {
	req := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "invalid-ip",
	}

	_, err := s.service.CreateNode(s.ctx, req)
	s.Error(err)
	s.ErrorIs(err, ErrInvalidNodeIP)
}

func (s *NodeServiceTestSuite) TestGetNode() {
	// Create a node first
	createReq := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	createResp, err := s.service.CreateNode(s.ctx, createReq)
	s.NoError(err)

	// Get the node
	node, err := s.service.GetNode(s.ctx, createResp.Node.ID)
	s.NoError(err)
	s.NotNil(node)
	s.Equal(createResp.Node.ID, node.ID)
	s.Equal("test-node", node.Name)
}

func (s *NodeServiceTestSuite) TestGetNode_NotFound() {
	_, err := s.service.GetNode(s.ctx, "non-existent-id")
	s.Error(err)
	s.ErrorIs(err, ErrNodeNotFound)
}

func (s *NodeServiceTestSuite) TestListNodes() {
	// Create multiple nodes
	for i := 0; i < 3; i++ {
		req := &CreateNodeRequest{
			Name:      fmt.Sprintf("test-node-%d", i),
			IPAddress: fmt.Sprintf("192.168.1.%d", 100+i),
		}
		_, err := s.service.CreateNode(s.ctx, req)
		s.NoError(err)
	}

	nodes, err := s.service.ListNodes(s.ctx, nil, 0, 0)
	s.NoError(err)
	s.Len(nodes, 3)
}

func (s *NodeServiceTestSuite) TestUpdateNode() {
	// Create a node first
	createReq := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	createResp, err := s.service.CreateNode(s.ctx, createReq)
	s.NoError(err)

	// Update the node
	status := models.NodeStatusActive
	updateReq := &UpdateNodeRequest{
		Name:   "updated-node",
		Status: &status,
	}

	updated, err := s.service.UpdateNode(s.ctx, createResp.Node.ID, updateReq)
	s.NoError(err)
	s.Equal("updated-node", updated.Name)
	s.Equal(models.NodeStatusActive, updated.Status)
}

func (s *NodeServiceTestSuite) TestUpdateNode_NotFound() {
	status := models.NodeStatusActive
	updateReq := &UpdateNodeRequest{
		Status: &status,
	}

	_, err := s.service.UpdateNode(s.ctx, "non-existent-id", updateReq)
	s.Error(err)
	s.ErrorIs(err, ErrNodeNotFound)
}

func (s *NodeServiceTestSuite) TestDeleteNode() {
	// Create a node first
	createReq := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	createResp, err := s.service.CreateNode(s.ctx, createReq)
	s.NoError(err)

	// Delete the node
	opts := &DeleteNodeCascadeOptions{}
	err = s.service.DeleteNode(s.ctx, createResp.Node.ID, opts)
	s.NoError(err)

	// Verify node is deleted
	_, err = s.service.GetNode(s.ctx, createResp.Node.ID)
	s.Error(err)
	s.ErrorIs(err, ErrNodeNotFound)
}

func (s *NodeServiceTestSuite) TestDeleteNode_NotFound() {
	opts := &DeleteNodeCascadeOptions{}
	err := s.service.DeleteNode(s.ctx, "non-existent-id", opts)
	s.Error(err)
	s.ErrorIs(err, ErrNodeNotFound)
}

func (s *NodeServiceTestSuite) TestRegenerateToken() {
	// Create a node first
	createReq := &CreateNodeRequest{
		Name:      "test-node",
		IPAddress: "192.168.1.100",
	}

	createResp, err := s.service.CreateNode(s.ctx, createReq)
	s.NoError(err)

	originalToken := createResp.Token

	// Regenerate token
	newToken, err := s.service.RegenerateToken(s.ctx, createResp.Node.ID)
	s.NoError(err)
	s.NotEmpty(newToken)
	s.NotEqual(originalToken, newToken)
	s.Len(newToken, 64)
}

func (s *NodeServiceTestSuite) TestRegenerateToken_NotFound() {
	_, err := s.service.RegenerateToken(s.ctx, "non-existent-id")
	s.Error(err)
	s.ErrorIs(err, ErrNodeNotFound)
}

func (s *NodeServiceTestSuite) TestGenerateToken() {
	token, err := s.service.generateToken()
	s.NoError(err)
	s.NotEmpty(token)
	s.Len(token, 64) // 32 bytes = 64 hex chars

	// Ensure tokens are unique
	token2, err := s.service.generateToken()
	s.NoError(err)
	s.NotEqual(token, token2)
}

func (s *NodeServiceTestSuite) TestCheckNodeHealth_InvalidIP() {
	node := &models.Node{
		Name:      "test-node",
		IPAddress: "invalid-ip",
	}

	online, err := s.service.CheckNodeHealth(s.ctx, node)
	s.False(online)
	s.NoError(err) // Not an error, just offline
}

func (s *NodeServiceTestSuite) TestCheckNodeHealth_Unreachable() {
	// Use a non-routable IP that will timeout
	node := &models.Node{
		Name:      "test-node",
		IPAddress: "192.0.2.1", // TEST-NET-1, should not be reachable
	}

	// Set a short timeout for the test
	s.service.SetTimeout(100 * time.Millisecond)

	online, err := s.service.CheckNodeHealth(s.ctx, node)
	s.False(online)
	s.NoError(err) // Not an error, just offline
}

func (s *NodeServiceTestSuite) TestValidateNodeIP_InvalidIP() {
	err := s.service.ValidateNodeIP(s.ctx, "invalid-ip")
	s.Error(err)
	s.ErrorIs(err, ErrInvalidNodeIP)
}

func TestNodeServiceTestSuite(t *testing.T) {
	suite.Run(t, new(NodeServiceTestSuite))
}

// Benchmark tests

func BenchmarkGenerateToken(b *testing.B) {
	service := &NodeService{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := service.generateToken()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCreateNode(b *testing.B) {
	db, _ := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	db.AutoMigrate(&models.Node{})
	defer func() {
		sqlDB, _ := db.DB()
		sqlDB.Close()
	}()

	repo := repository.NewNodeRepository(db)
	service := NewNodeService(repo)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := &CreateNodeRequest{
			Name:      fmt.Sprintf("bench-node-%d", i),
			IPAddress: fmt.Sprintf("192.168.%d.%d", i/256, i%256),
		}
		_, err := service.CreateNode(ctx, req)
		if err != nil {
			b.Fatal(err)
		}
	}
}
