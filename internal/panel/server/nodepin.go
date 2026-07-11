package server

import (
	"context"

	"github.com/maburvm/panel/internal/panel/client"
	"github.com/maburvm/panel/internal/panel/repository"
)

// nodePinStore adapts NodeRepository to client.PinStore so agent-TLS certificate
// pinning is persisted in the nodes table. The client.PinStore interface is
// context-free (it's called from inside the TLS handshake), so we use a
// background context for the DB access.
type nodePinStore struct {
	repo *repository.NodeRepository
}

func newNodePinStore(repo *repository.NodeRepository) client.PinStore {
	return &nodePinStore{repo: repo}
}

func (s *nodePinStore) GetFingerprint(nodeID string) string {
	fp, err := s.repo.GetCertFingerprint(context.Background(), nodeID)
	if err != nil {
		return ""
	}
	return fp
}

func (s *nodePinStore) SetFingerprint(nodeID, fingerprint string) {
	_ = s.repo.SetCertFingerprint(context.Background(), nodeID, fingerprint)
}
