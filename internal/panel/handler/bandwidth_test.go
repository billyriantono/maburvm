package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newTestBandwidthService builds a BandwidthService backed by the test DB so
// owner/admin tests reach the service layer (not just pass authz).
func newTestBandwidthService(db *gorm.DB) *service.BandwidthService {
	return service.NewBandwidthService(repository.NewBandwidthUsageRepository(db), nil)
}

// TestBandwidthHandler_OwnerAuthorized verifies an owner reaches the service
// layer (no 401/404 short-circuit) on the usage read.
func TestBandwidthHandler_OwnerAuthorized(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewBandwidthHandler(newTestBandwidthService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMBandwidth(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBandwidthHandler_OtherUserGets404 verifies a non-owner maps to 404.
func TestBandwidthHandler_OtherUserGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewBandwidthHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, other, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMBandwidth(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestBandwidthHandler_NonexistentVMGets404 verifies a nonexistent VM maps to 404.
func TestBandwidthHandler_NonexistentVMGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	h := NewBandwidthHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.GetVMBandwidth(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestBandwidthHandler_UnauthenticatedGets401 verifies missing auth maps to 401.
func TestBandwidthHandler_UnauthenticatedGets401(t *testing.T) {
	db := newAuthzTestDB(t)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))

	h := NewBandwidthHandler(nil, authz.NewAuthorizer(db))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMBandwidth(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestBandwidthHandler_AdminBypassesOwnership verifies an admin reaches the
// service layer for another user's VM.
func TestBandwidthHandler_AdminBypassesOwnership(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)

	h := NewBandwidthHandler(newTestBandwidthService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMBandwidth(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestBandwidthHandler_QuotaRequiresAdmin verifies the quota endpoint is an
// admin-only boundary: a non-admin client gets 403, an admin proceeds.
func TestBandwidthHandler_QuotaRequiresAdmin(t *testing.T) {
	db := newAuthzTestDB(t)
	client := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, client)

	h := NewBandwidthHandler(newTestBandwidthService(db), authz.NewAuthorizer(db))

	// Non-admin.
	c, rec := ctxWithUser(t, db, client, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	require.NoError(t, h.SetVMBandwidthQuota(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	// Admin proceeds past the authz boundary and reaches the service.
	c2, rec2 := ctxWithUser(t, db, admin, models.RoleAdmin)
	c2.SetParamNames("id")
	c2.SetParamValues(vmID)
	require.NoError(t, h.SetVMBandwidthQuota(c2))
	assert.Equal(t, http.StatusOK, rec2.Code)
}
