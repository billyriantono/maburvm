package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func setupVMFilterDB(t *testing.T) *VMRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:vmfilter-%s?mode=memory&cache=shared", t.Name())),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE vms (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		node_id TEXT NOT NULL,
		hostname TEXT NOT NULL,
		os_template_id TEXT NOT NULL DEFAULT '',
		resources TEXT NOT NULL DEFAULT '{}',
		status TEXT NOT NULL DEFAULT 'stopped',
		source_migration TEXT,
		vnc_port INTEGER,
		vnc_password TEXT,
		console_enabled INTEGER NOT NULL DEFAULT 1,
		rescue_mode INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		deleted_at DATETIME
	)`).Error)

	seed := []models.VM{
		{ID: "11111111-1111-1111-1111-111111111111", UserID: "alice", NodeID: "node-a", Hostname: "wg.pranava.co.id", Status: models.VMStatusRunning},
		{ID: "22222222-2222-2222-2222-222222222222", UserID: "alice", NodeID: "node-b", Hostname: "db.pranava.co.id", Status: models.VMStatusStopped},
		{ID: "33333333-3333-3333-3333-333333333333", UserID: "bob", NodeID: "node-a", Hostname: "WG-Backup.example.net", Status: models.VMStatusRunning},
	}
	for i := range seed {
		require.NoError(t, db.Create(&seed[i]).Error)
	}
	return NewVMRepository(db)
}

func hostnames(vms []models.VM) []string {
	out := make([]string, 0, len(vms))
	for _, vm := range vms {
		out = append(out, vm.Hostname)
	}
	return out
}

// The reported bug: searching a hostname returned nothing, because the filter
// ran in the browser over the ten rows of the current page.
func TestListFilteredSearchesEveryPage(t *testing.T) {
	repo := setupVMFilterDB(t)
	ctx := context.Background()

	vms, err := repo.ListFiltered(ctx, VMFilter{Search: "wg.pranava.co.id"}, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"wg.pranava.co.id"}, hostnames(vms))

	total, err := repo.CountFiltered(ctx, VMFilter{Search: "wg.pranava.co.id"})
	require.NoError(t, err)
	require.EqualValues(t, 1, total, "the total must reflect the search, or the pager offers pages that are empty")
}

func TestListFilteredSearchIsCaseInsensitiveSubstring(t *testing.T) {
	repo := setupVMFilterDB(t)

	vms, err := repo.ListFiltered(context.Background(), VMFilter{Search: "wg"}, 10, 0)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"wg.pranava.co.id", "WG-Backup.example.net"}, hostnames(vms))
}

// A UUID is what an operator pastes from a URL or a log line.
func TestListFilteredSearchMatchesVMID(t *testing.T) {
	repo := setupVMFilterDB(t)

	vms, err := repo.ListFiltered(context.Background(), VMFilter{Search: "22222222-2222-2222-2222-222222222222"}, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"db.pranava.co.id"}, hostnames(vms))
}

// The second bug: filters were a switch, so only the first one applied. For a
// customer the owner filter is always set, which silently discarded whatever
// status or node they had chosen.
func TestListFilteredCombinesEveryFilter(t *testing.T) {
	repo := setupVMFilterDB(t)
	ctx := context.Background()

	vms, err := repo.ListFiltered(ctx, VMFilter{UserID: "alice", Status: models.VMStatusRunning}, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"wg.pranava.co.id"}, hostnames(vms),
		"alice has two VMs and one is stopped; the status filter must not be dropped")

	vms, err = repo.ListFiltered(ctx, VMFilter{UserID: "alice", NodeID: "node-a", Search: "pranava"}, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"wg.pranava.co.id"}, hostnames(vms))

	// Tenant isolation must survive a search that would otherwise match.
	vms, err = repo.ListFiltered(ctx, VMFilter{UserID: "alice", Search: "wg"}, 10, 0)
	require.NoError(t, err)
	require.Equal(t, []string{"wg.pranava.co.id"}, hostnames(vms),
		"bob's WG-Backup must never appear in alice's results")
}

func TestListFilteredNoFiltersReturnsEverything(t *testing.T) {
	repo := setupVMFilterDB(t)

	total, err := repo.CountFiltered(context.Background(), VMFilter{})
	require.NoError(t, err)
	require.EqualValues(t, 3, total)
}
