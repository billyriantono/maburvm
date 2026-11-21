package handler

import (
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Deleting an imported VM's own volumes is a storage deletion: staff without
// the right to delete storage are refused before anything is queued.
func TestDeleteVMImportedVolumesNeedsStorageDelete(t *testing.T) {
	db := newVMRouteTestDB(t)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, admin)

	h := NewVMHandler(nil, nil, nil, nil, nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(admin), Role: models.RoleAdmin, Permissions: []string{"vm:delete"}})
	c.Request().URL.RawQuery = "delete_imported_volumes=true"
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.DeleteVM(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "permission to delete storage")
}
