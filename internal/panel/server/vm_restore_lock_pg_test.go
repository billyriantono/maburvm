//go:build pg

package server

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/queue"
)

// A restore holds its VM, against a start among others, for as long as its job
// is queued or running, and lets go however the job ends: the hold is the
// job's state in the queue, so there is nothing to release by hand.
func TestRestoreHoldsTheVMUntilItsJobEnds(t *testing.T) {
	db, raw, dsn := pgMigratedDB(t)
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	defer pool.Close()
	require.NoError(t, queue.RunMigrations(context.Background(), pool))
	const vm = "55555555-5555-5555-5555-555555555555"
	svc := service.NewVMService(db, repository.NewVMRepository(db), repository.NewNodeRepository(db),
		repository.NewTemplateRepository(db), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	start := func() error {
		_, err := svc.ExecuteLifecycleCommand(context.Background(), &service.LifecycleRequest{VMID: vm, Command: service.LifecycleStart})
		return err
	}
	job := func(kind, operation, state string) int64 {
		var id int64
		require.NoError(t, raw.QueryRow(`INSERT INTO river_job (args, kind, max_attempts, queue, state)
			VALUES (jsonb_build_object('vm_id', $1::text, 'operation', $2::text), $3, 3, 'critical', $4) RETURNING id`,
			vm, operation, kind, state).Scan(&id))
		return id
	}
	end := func(id int64, state string) {
		_, err := raw.Exec(`UPDATE river_job SET state = $1, finalized_at = now() WHERE id = $2`, state, id)
		require.NoError(t, err)
	}

	// No VM row: a start that gets past the hold stops at "not found".
	require.ErrorIs(t, start(), service.ErrVMNotFound)

	backup := job("restore", "", "running")
	require.ErrorIs(t, start(), service.ErrVMRestoring)
	end(backup, "cancelled")
	require.ErrorIs(t, start(), service.ErrVMNotFound, "a cancelled restore holds nothing")

	snap := job("snapshot_operation", "restore", "available")
	job("snapshot_operation", "create", "available")
	require.ErrorIs(t, start(), service.ErrVMRestoring)
	end(snap, "completed")
	require.ErrorIs(t, start(), service.ErrVMNotFound, "a snapshot being taken is no restore")
}
