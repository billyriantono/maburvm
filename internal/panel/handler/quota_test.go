package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/repository"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// stubQuotaService is an in-memory stub implementing the quotaService interface
// so the handler error-mapping contract can be tested without a live DB.
type stubQuotaService struct {
	getStatusErr error
	setQuotaErr  error
	status       *service.QuotaStatus
	quota        *models.UserQuota
}

func (s *stubQuotaService) GetStatus(ctx context.Context, userID string) (*service.QuotaStatus, error) {
	if s.getStatusErr != nil {
		return nil, s.getStatusErr
	}
	if s.status != nil {
		return s.status, nil
	}
	return &service.QuotaStatus{}, nil
}

func (s *stubQuotaService) SetQuota(ctx context.Context, userID string, req *service.SetQuotaRequest) (*models.UserQuota, error) {
	if s.setQuotaErr != nil {
		return nil, s.setQuotaErr
	}
	if s.quota != nil {
		return s.quota, nil
	}
	return &models.UserQuota{UserID: userID}, nil
}

// ctxWithUser builds an echo context with the authenticated user set (as the
// RequireAuth middleware would), so GetMyQuota resolves identity.
func quotaCtxWithUser(t *testing.T, userID string, role models.UserRole) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(userID), Role: role})
	return c, rec
}

// TestQuotaHandler_GetMyQuota_NotAvailable_MapsTo409 verifies the expected
// managed-account pending/unprovisioned state maps to 409 (not 500) with a
// generic code and no policy/cap leakage.
func TestQuotaHandler_GetMyQuota_NotAvailable_MapsTo409(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{getStatusErr: service.ErrQuotaNotAvailable})
	c, rec := quotaCtxWithUser(t, uuid.New().String(), models.RoleClient)

	require.NoError(t, h.GetMyQuota(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "quota_not_available")
	assert.NotContains(t, rec.Body.String(), "managed")
	assert.NotContains(t, rec.Body.String(), "policy")
}

// TestQuotaHandler_GetUserQuota_NotAvailable_MapsTo409 verifies the admin GET
// endpoint maps the same expected state to 409 with the generic code.
func TestQuotaHandler_GetUserQuota_NotAvailable_MapsTo409(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{getStatusErr: service.ErrQuotaNotAvailable})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.GetUserQuota(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "quota_not_available")
}

// TestQuotaHandler_SetUserQuota_ManagedDirectMutation_MapsTo409 verifies the
// admin SetUserQuota maps ErrManagedQuotaDirectMutation to 409 with a generic
// code (the direct legacy endpoint cannot alter managed accounts).
func TestQuotaHandler_SetUserQuota_ManagedDirectMutation_MapsTo409(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{setQuotaErr: repository.ErrManagedQuotaDirectMutation})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.SetUserQuota(c))
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "managed_quota_direct_mutation")
}

// TestQuotaHandler_SetUserQuota_UserNotFound_MapsTo404 verifies a direct legacy
// SetUserQuota for an absent target user (ErrUserNotFound from the repository
// Upsert) maps to 404 with the generic user_not_found code and no policy/cap/DB
// leakage.
func TestQuotaHandler_SetUserQuota_UserNotFound_MapsTo404(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{setQuotaErr: repository.ErrUserNotFound})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.SetUserQuota(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "user_not_found")
	assert.Contains(t, rec.Body.String(), "User not found")
	assert.NotContains(t, rec.Body.String(), "managed")
	assert.NotContains(t, rec.Body.String(), "policy")
}

// TestQuotaHandler_GetUserQuota_RecordNotFound_MapsTo404 verifies an admin GET
// that surfaces a bare gorm.ErrRecordNotFound (non-existent user) maps to 404
// with the same generic user_not_found code, matched by error identity rather
// than by string.
func TestQuotaHandler_GetUserQuota_RecordNotFound_MapsTo404(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{getStatusErr: gorm.ErrRecordNotFound})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())

	require.NoError(t, h.GetUserQuota(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Contains(t, rec.Body.String(), "user_not_found")
	assert.Contains(t, rec.Body.String(), "User not found")
}

// TestQuotaHandler_GetMyQuota_UnexpectedError_MapsTo500 verifies that an
// unexpected error (not the known managed states) remains a 500 and does not
// leak a 409-style generic code.
func TestQuotaHandler_GetMyQuota_UnexpectedError_MapsTo500(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{getStatusErr: errors.New("db connection lost")})
	c, rec := quotaCtxWithUser(t, uuid.New().String(), models.RoleClient)

	require.NoError(t, h.GetMyQuota(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.NotContains(t, rec.Body.String(), "quota_not_available")
}

// TestQuotaHandler_GetMyQuota_UnknownErrorWrappedStays500 verifies a wrapped
// unexpected error (wrapping neither known sentinel) still maps to 500.
func TestQuotaHandler_GetMyQuota_UnknownErrorWrappedStays500(t *testing.T) {
	wrapped := errors.New("inner: " + service.ErrQuotaNotAvailable.Error() + " extra context")
	h := NewQuotaHandler(&stubQuotaService{getStatusErr: wrapped})
	c, rec := quotaCtxWithUser(t, uuid.New().String(), models.RoleClient)

	require.NoError(t, h.GetMyQuota(c))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// TestQuotaHandler_GetMyQuota_Success verifies a successful lookup returns 200
// with the success/data envelope.
func TestQuotaHandler_GetMyQuota_Success(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{status: &service.QuotaStatus{}})
	c, rec := quotaCtxWithUser(t, uuid.New().String(), models.RoleClient)

	require.NoError(t, h.GetMyQuota(c))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"success":true`)
}

// TestQuotaHandler_GetMyQuota_Unauthenticated verifies a missing self identity
// maps to 401 with no service call.
func TestQuotaHandler_GetMyQuota_Unauthenticated(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.GetMyQuota(c))
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestQuotaHandler_SetUserQuota_InvalidBody_MapsTo400 verifies an unbindable
// body stays 400.
func TestQuotaHandler_SetUserQuota_InvalidBody_MapsTo400(t *testing.T) {
	h := NewQuotaHandler(&stubQuotaService{})
	e := echo.New()
	req := httptest.NewRequest(http.MethodPut, "/", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(uuid.New().String())
	// Force a bind failure by setting an unreadable body.
	badReq := httptest.NewRequest(http.MethodPut, "/", errReader{})
	c.SetRequest(badReq)

	require.NoError(t, h.SetUserQuota(c))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

// errReader is a request body that always fails to read, forcing c.Bind to error.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read error") }
