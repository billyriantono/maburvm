package handler

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/shared/models"
)

// Suspension is the provider withholding service. The owner of a suspended VM
// must not be able to lift it with an action they are otherwise entitled to.
func TestSuspendedVM_ClientCannotChangeIt(t *testing.T) {
	db := newVMRouteTestDB(t)
	h := newTestVMHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", vmID).Update("status", models.VMStatusSuspended).Error)

	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	assert.False(t, h.authorizeVMChange(c, vmID), "owner of a suspended VM must be refused")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "suspended")

	c, _ = ctxWithUser(t, db, admin, models.RoleAdmin)
	assert.True(t, h.authorizeVMChange(c, vmID), "an administrator still can")

	c, _ = ctxWithUser(t, db, owner, models.RoleClient)
	assert.True(t, h.authorizeVM(c, vmID), "reading and deleting stay allowed")

	// Once service is restored the owner is back to normal.
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", vmID).Update("status", models.VMStatusStopped).Error)
	c, _ = ctxWithUser(t, db, owner, models.RoleClient)
	assert.True(t, h.authorizeVMChange(c, vmID))
}

// A customer never suspends or unsuspends, whatever state the VM is in.
func TestClientCannotSuspendOrUnsuspend(t *testing.T) {
	db := newVMRouteTestDB(t)
	h := newTestVMHandler(t, db)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	require.NoError(t, db.Model(&models.VM{}).Where("id = ?", vmID).Update("status", models.VMStatusRunning).Error)

	for name, call := range map[string]func() int{
		"suspend": func() int {
			c, rec := ctxWithUser(t, db, owner, models.RoleClient)
			c.SetParamNames("id")
			c.SetParamValues(vmID)
			require.NoError(t, h.SuspendVM(c))
			return rec.Code
		},
		"unsuspend": func() int {
			c, rec := ctxWithUser(t, db, owner, models.RoleClient)
			c.SetParamNames("id")
			c.SetParamValues(vmID)
			require.NoError(t, h.UnsuspendVM(c))
			return rec.Code
		},
	} {
		assert.Equal(t, http.StatusForbidden, call(), name)
	}
}
