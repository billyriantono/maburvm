package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

func groupCtx(method, body string, user *middleware.UserContext, params ...string) (echo.Context, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, "/", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := echo.New().NewContext(req, rec)
	c.Set(middleware.UserContextKey, user)
	for i := 0; i+1 < len(params); i += 2 {
		c.SetParamNames(append(c.ParamNames(), params[i])...)
		c.SetParamValues(append(c.ParamValues(), params[i+1])...)
	}
	return c, rec
}

// Customers keep their own groups apart; staff need the right to manage groups
// to create one for everyone; a customer cannot attach a group to a VM that
// is not theirs.
func TestFirewallGroupHandlerScopes(t *testing.T) {
	db := newAuthzTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS firewall_groups (id TEXT PRIMARY KEY, user_id TEXT, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', created_at DATETIME, updated_at DATETIME)`).Error)
	h := NewFirewallGroupHandler(service.NewFirewallGroupService(db, nil), authz.NewAuthorizer(db))
	alice, bob := seedUser(t, db, models.RoleClient), seedUser(t, db, models.RoleClient)
	asAlice := &middleware.UserContext{ID: uuid.MustParse(alice), Role: models.RoleClient}
	asBob := &middleware.UserContext{ID: uuid.MustParse(bob), Role: models.RoleClient}
	readOnlyStaff := &middleware.UserContext{ID: uuid.New(), Role: models.RoleAdmin, Permissions: []string{"firewall_group:read", "vm:update"}}

	c, rec := groupCtx(http.MethodPost, `{"name":"Web"}`, asAlice)
	require.NoError(t, h.Create(c))
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct{ Data models.FirewallGroup }
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))

	c, rec = groupCtx(http.MethodGet, "", asBob)
	require.NoError(t, h.List(c))
	assert.JSONEq(t, `{"success":true,"data":[]}`, rec.Body.String())
	c, rec = groupCtx(http.MethodGet, "", asBob, "id", created.Data.ID)
	require.NoError(t, h.Get(c))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	c, rec = groupCtx(http.MethodPost, `{"name":"Everyone"}`, readOnlyStaff)
	require.NoError(t, h.Create(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)

	bobsVM := seedVM(t, db, bob)
	c, rec = groupCtx(http.MethodPost, `{"group_id":"`+created.Data.ID+`"}`, asAlice, "id", bobsVM)
	require.NoError(t, h.Attach(c))
	assert.Equal(t, http.StatusNotFound, rec.Code, "the VM is not Alice's")
	c, rec = groupCtx(http.MethodPost, `{"group_id":"`+created.Data.ID+`"}`, asBob, "id", bobsVM)
	require.NoError(t, h.Attach(c))
	assert.Equal(t, http.StatusNotFound, rec.Code, "the group is not Bob's")
}
