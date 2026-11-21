package handler

import (
	"net/http"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// A change refused because the VM is moving to another node is a conflict that
// clears by itself, not a server fault: every route says 409, whatever status
// it answers its other failures with.
func TestChangesToAMovingVMAreConflicts(t *testing.T) {
	db := newVMRouteTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS vm_migrations (id TEXT PRIMARY KEY, vm_id TEXT, status TEXT)`).Error)
	h := newTestVMHandler(t, db)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))
	require.NoError(t, db.Exec(`INSERT INTO vm_migrations (id, vm_id, status) VALUES ('m1', ?, ?)`, vmID, models.MigrationRunning).Error)

	for _, c := range []struct {
		name string
		call func(*VMHandler, echo.Context) error
	}{
		{"start", (*VMHandler).StartVM},
		{"stop", (*VMHandler).StopVM},
		{"restart", (*VMHandler).RestartVM},
		{"suspend", (*VMHandler).SuspendVM},
		{"unsuspend", (*VMHandler).UnsuspendVM},
		{"delete", (*VMHandler).DeleteVM},
		{"detach ISO", (*VMHandler).DetachISOVM},
		{"leave rescue", (*VMHandler).UnrescueVM},
		{"detach disk", (*VMHandler).DetachVMDisk},
	} {
		ctx, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
		ctx.SetParamNames("id", "device")
		ctx.SetParamValues(vmID, "vdb")
		require.NoError(t, c.call(h, ctx), c.name)
		assert.Equal(t, http.StatusConflict, rec.Code, "%s: %s", c.name, rec.Body.String())
		assert.Contains(t, rec.Body.String(), "vm_migrating", c.name)
	}
}
