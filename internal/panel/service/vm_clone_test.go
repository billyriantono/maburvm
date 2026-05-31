package service

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
)

// CloneVM must refuse to clone a running VM (a live qcow2 copy would be
// inconsistent) and must report a missing source clearly — both before reaching
// the create pipeline (riverClient is nil here, so reaching it would panic).
func TestCloneVMGuards(t *testing.T) {
	db := setupVMIPAMTestDB(t)
	ctx := context.Background()

	user, node, tmpl := seedVMIPAMDependencies(t, db)
	running := &models.VM{UserID: user.ID.String(), NodeID: node.ID, Hostname: "src.example.test", OSTemplateID: tmpl.ID, Resources: models.Resources{CPU: 1, RAM: 512, Disk: 10}, Status: models.VMStatusRunning}
	require.NoError(t, db.Create(running).Error)

	svc := NewVMService(
		db,
		repository.NewVMRepository(db),
		repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)

	// Running source → rejected.
	_, err := svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: running.ID})
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be stopped")

	// Unknown source → not found.
	_, err = svc.CloneVM(ctx, &CloneVMRequest{SourceVMID: "00000000-0000-0000-0000-000000000000"})
	require.ErrorIs(t, err, ErrVMNotFound)
}
