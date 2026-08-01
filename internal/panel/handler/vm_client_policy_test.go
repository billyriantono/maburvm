package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// setBody binds a JSON body onto an echo context for handler tests. Echo's Bind
// reads the underlying http.Request body, so we replace the request entirely with
// an httptest request carrying the JSON payload (the documented, working pattern).
func setBody(c echo.Context, v interface{}) {
	b, _ := json.Marshal(v)
	req := httptest.NewRequest(http.MethodPost, "/", io.NopCloser(bytes.NewReader(b)))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c.SetRequest(req)
}

// TestVMCreate_ClientProhibitedSelection verifies a RoleClient supplying any
// explicit infrastructure/network choice is rejected with the generic 403 and
// never reaches the service (nil service → would panic if it did).
func TestVMCreate_ClientProhibitedSelection(t *testing.T) {
	db := newVMRouteTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	prohibited := map[string]CreateVMRequest{
		"node_id":         {Hostname: "vm", OSTemplateID: uuid.New().String(), NodeID: uuid.New().String()},
		"ip_pool_id":      {Hostname: "vm", OSTemplateID: uuid.New().String(), IPPoolID: uuid.New().String()},
		"requested_ip":    {Hostname: "vm", OSTemplateID: uuid.New().String(), RequestedIP: "10.0.0.5"},
		"managed_network": {Hostname: "vm", OSTemplateID: uuid.New().String(), ManagedNetworkID: uuid.New().String()},
		"cpu_model":       {Hostname: "vm", OSTemplateID: uuid.New().String(), CPUModel: "host-passthrough"},
		"bandwidth":       {Hostname: "vm", OSTemplateID: uuid.New().String(), BandwidthMbps: 500},
		"vlan":            {Hostname: "vm", OSTemplateID: uuid.New().String(), VLANID: 100},
	}

	h := NewVMHandler(nil, nil, nil, nil, nil, authz.NewAuthorizer(db))

	for name, req := range prohibited {
		t.Run(name, func(t *testing.T) {
			c, rec := ctxWithUser(t, db, owner, models.RoleClient)
			createReq := &service.CreateVMRequest{}
			denied := h.applyClientVMPolicy(c, &middleware.UserContext{ID: uuid.MustParse(owner), Role: models.RoleClient}, &req, createReq)
			require.True(t, denied, "client prohibited field %s should be denied", name)
			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
		})
	}
}

// TestVMCreate_ClientPermittedForcesAutoAssignIP verifies a RoleClient request
// with no prohibited selection is NOT denied and forces AutoAssignIP=true.
func TestVMCreate_ClientPermittedForcesAutoAssignIP(t *testing.T) {
	db := newVMRouteTestDB(t)
	owner := seedUser(t, db, models.RoleClient)

	h := NewVMHandler(nil, nil, nil, nil, nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)

	req := CreateVMRequest{
		Hostname:     "vm",
		OSTemplateID: uuid.New().String(),
		PlanID:       uuid.New().String(), // plan/template selection stays allowed
	}
	createReq := &service.CreateVMRequest{}
	denied := h.applyClientVMPolicy(c, &middleware.UserContext{ID: uuid.MustParse(owner), Role: models.RoleClient}, &req, createReq)
	require.False(t, denied, "client auto-only request must not be denied")
	assert.Equal(t, http.StatusOK, rec.Code) // handler wrote nothing
	assert.True(t, createReq.AutoAssignIP, "client request must force AutoAssignIP")
}

// TestVMCreate_AdminBypassesPolicy verifies an admin is never denied and does
// NOT get AutoAssignIP forced (admin controls placement).
func TestVMCreate_AdminBypassesPolicy(t *testing.T) {
	db := newVMRouteTestDB(t)
	admin := seedUser(t, db, models.RoleAdmin)

	h := NewVMHandler(nil, nil, nil, nil, nil, authz.NewAuthorizer(db))
	c, rec := ctxWithUser(t, db, admin, models.RoleAdmin)

	req := CreateVMRequest{
		Hostname:     "vm",
		OSTemplateID: uuid.New().String(),
		NodeID:       uuid.New().String(), // admin may pick a node
	}
	createReq := &service.CreateVMRequest{}
	denied := h.applyClientVMPolicy(c, &middleware.UserContext{ID: uuid.MustParse(admin), Role: models.RoleAdmin}, &req, createReq)
	require.False(t, denied, "admin must not be denied")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.False(t, createReq.AutoAssignIP, "admin placement must not force AutoAssignIP")
}

// TestVMClone_ClientDestNodeDenied verifies a client clone with dest_node_id is
// rejected with the generic 403 before the service runs.
func TestVMClone_ClientDestNodeDenied(t *testing.T) {
	db := newVMRouteTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestVMHandler(t, db)
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBody(c, map[string]interface{}{"dest_node_id": uuid.New().String()})

	require.NoError(t, h.CloneVM(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestVMClone_ClientNoDestNodePassesPolicy verifies a client clone WITHOUT a
// destination node is not denied by the policy (it then proceeds to the service).
func TestVMClone_ClientNoDestNodePassesPolicy(t *testing.T) {
	db := newVMRouteTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestVMHandler(t, db)
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBody(c, map[string]interface{}{"hostname": "clone"})

	require.NoError(t, h.CloneVM(c))
	assert.NotEqual(t, http.StatusForbidden, rec.Code)
	assert.NotContains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}

// TestVMMigrate_ClientDenied verifies a client migration is rejected with the
// generic 403.
func TestVMMigrate_ClientDenied(t *testing.T) {
	db := newVMRouteTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)

	h := newTestVMHandler(t, db)
	c, rec := ctxWithUser(t, db, owner, models.RoleClient)
	c.SetParamNames("id")
	c.SetParamValues(vmID)
	setBody(c, map[string]interface{}{"dest_node_id": uuid.New().String()})

	require.NoError(t, h.MigrateVM(c))
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "may not select VM network or infrastructure placement")
}
