package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// fakeStorageService is a no-op StorageService used to exercise the authz
// boundary without provisioning real volumes.
type fakeStorageService struct{}

func (fakeStorageService) GetPools() ([]models.StoragePool, error)            { return nil, nil }
func (fakeStorageService) GetPoolByID(id string) (*models.StoragePool, error) { return nil, nil }
func (fakeStorageService) GetPoolsByNodeID(nodeID string) ([]models.StoragePool, error) {
	return nil, nil
}
func (fakeStorageService) CreatePool(pool *models.StoragePool) error            { return nil }
func (fakeStorageService) UpdatePool(id string, pool *models.StoragePool) error { return nil }
func (fakeStorageService) DeletePool(id string) error                           { return nil }
func (fakeStorageService) GetVolumes(poolID string) ([]models.StorageVolume, error) {
	return nil, nil
}
func (fakeStorageService) GetVolumeByID(id string) (*models.StorageVolume, error) { return nil, nil }
func (fakeStorageService) CreateVolume(volume *models.StorageVolume) error        { return nil }
func (fakeStorageService) DeleteVolume(id string) error                           { return nil }

// TestStorageHandler_NonAdminDenied verifies host storage pool/volume operations
// are admin-only: an authenticated client gets 403 and never reaches the service.
func TestStorageHandler_NonAdminDenied(t *testing.T) {
	db := newAuthzTestDB(t)
	client := seedUser(t, db, models.RoleClient)

	h := NewStorageHandler(fakeStorageService{}, authz.NewAuthorizer(db))

	for _, tc := range []struct {
		name string
		fn   func(c echo.Context) error
	}{
		{"GetPools", h.GetPools},
		{"GetPoolByID", h.GetPoolByID},
		{"GetPoolsByNodeID", h.GetPoolsByNodeID},
		{"CreatePool", h.CreatePool},
		{"UpdatePool", h.UpdatePool},
		{"DeletePool", h.DeletePool},
		{"GetVolumes", h.GetVolumes},
		{"GetVolumeByID", h.GetVolumeByID},
		{"CreateVolume", h.CreateVolume},
		{"DeleteVolume", h.DeleteVolume},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, rec := ctxWithUser(t, db, client, models.RoleClient)
			require.NoError(t, tc.fn(c))
			assert.Equal(t, http.StatusForbidden, rec.Code, "non-admin must be denied")
		})
	}
}

// TestStorageHandler_AdminAllowed verifies an admin passes the authz boundary
// and reaches the service layer (which is a no-op stub here).
func TestStorageHandler_AdminAllowed(t *testing.T) {
	db := newAuthzTestDB(t)
	admin := seedUser(t, db, models.RoleAdmin)

	h := NewStorageHandler(fakeStorageService{}, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
	require.NoError(t, h.GetPools(c))
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
	assert.NotEqual(t, http.StatusUnauthorized, rec.Code)
}

// TestStorageHandler_UnauthenticatedGets401 verifies missing auth maps to 401
// even for the admin-only storage boundary.
func TestStorageHandler_UnauthenticatedGets401(t *testing.T) {
	db := newAuthzTestDB(t)

	h := NewStorageHandler(fakeStorageService{}, authz.NewAuthorizer(db))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GetPools(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// ensure the middleware import is referenced (ctxWithUser relies on it via the
// shared helper in this package).
var _ = middleware.UserContextKey
var _ = uuid.New
