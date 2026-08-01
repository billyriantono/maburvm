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
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// newTestMetricsService builds a MetricsService backed by the test DB so owner/
// admin tests reach the service layer.
func newTestMetricsService(db *gorm.DB) *service.MetricsService {
	return service.NewMetricsService(db)
}

// TestMetricsHandler_OwnerAuthorized verifies an owner reaches the service
// layer for VM metric history (no 401/404 short-circuit).
func TestMetricsHandler_OwnerAuthorized(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewMetricsHandler(newTestMetricsService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMMetricsHistory(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestMetricsHandler_OtherUserGets404 verifies a non-owner maps to 404.
func TestMetricsHandler_OtherUserGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	other := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := NewMetricsHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, other, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMMetricsHistory(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestMetricsHandler_NonexistentVMGets404 verifies a nonexistent VM maps to 404.
func TestMetricsHandler_NonexistentVMGets404(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	h := NewMetricsHandler(nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.GetVMMetricsHistory(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// TestMetricsHandler_UnauthenticatedGets401 verifies missing auth maps to 401.
func TestMetricsHandler_UnauthenticatedGets401(t *testing.T) {
	db := newAuthzTestDB(t)
	vmID := seedVM(t, db, seedUser(t, db, models.RoleClient))

	h := NewMetricsHandler(nil, authz.NewAuthorizer(db))
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMMetricsHistory(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestMetricsHandler_AdminBypassesOwnership verifies an admin reaches the
// service layer for another user's VM metrics.
func TestMetricsHandler_AdminBypassesOwnership(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	admin := seedUser(t, db, models.RoleAdmin)
	vmID := seedVM(t, db, owner)

	h := NewMetricsHandler(newTestMetricsService(db), authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)
	c.SetParamNames("id")
	c.SetParamValues(vmID)

	require.NoError(t, h.GetVMMetricsHistory(c))
	assert.Equal(t, http.StatusOK, rec.Code)
}
