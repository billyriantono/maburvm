package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/maburvm/panel/internal/shared/models"
)

// The suspended field is read-only on the model, so an update through the
// model is dropped without an error. This checks the flag reaches the row.
func TestSetSuspendedIsStored(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:susp?mode=memory&cache=shared"),
		&gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	require.NoError(t, db.Exec(`CREATE TABLE vms (id TEXT PRIMARY KEY, suspended BOOLEAN NOT NULL DEFAULT 0)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO vms (id) VALUES ('vm-1')`).Error)

	s := &VMService{db: db}
	vm := &models.VM{ID: "vm-1"}
	stored := func() (on bool) {
		require.NoError(t, db.Raw(`SELECT suspended FROM vms WHERE id = 'vm-1'`).Scan(&on).Error)
		return on
	}

	require.NoError(t, s.setSuspended(context.Background(), vm, true))
	require.True(t, stored())
	require.True(t, vm.Suspended)

	require.NoError(t, s.setSuspended(context.Background(), vm, false))
	require.False(t, stored())
}
