package handler

import (
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
	"github.com/maburvm/panel/internal/shared/models"
)

// A customer, even on their own VM, joins only their own private networks:
// they cannot put a card on a pool's bridge, choose or change a MAC, or take
// off a card that is not on one of their VPCs.
func TestNICRoutesKeepCustomersToTheirOwnNetworks(t *testing.T) {
	db := newAuthzTestDB(t)
	owner := seedUser(t, db, models.RoleClient)
	vmID := seedVM(t, db, owner)
	h := NewNetworkHandler(newTestNetworkService(db), authz.NewAuthorizer(db))
	// A second card on a pool's bridge (not a VPC of theirs).
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS vm_nics (id TEXT PRIMARY KEY, vm_id TEXT NOT NULL,
		position INTEGER NOT NULL, bridge TEXT NOT NULL DEFAULT '', mac_address TEXT NOT NULL, created_at DATETIME, updated_at DATETIME)`).Error)
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS vm_nics") }) // the in-memory database is shared by the package's tests
	cardID := uuid.NewString()
	require.NoError(t, db.Create(&models.VMNIC{ID: cardID, VMID: vmID, Position: 1, Bridge: "br-pub2", MACAddress: "52:54:00:aa:00:01"}).Error)

	call := func(method, body string, params ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/", strings.NewReader(body))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()
		c := echo.New().NewContext(req, rec)
		c.Set(middleware.UserContextKey, &middleware.UserContext{ID: uuid.MustParse(owner), Role: models.RoleClient})
		c.SetParamNames(append([]string{"id"}, params[:len(params)/2]...)...)
		c.SetParamValues(append([]string{vmID}, params[len(params)/2:]...)...)
		switch {
		case method == http.MethodPost:
			require.NoError(t, h.AddNIC(c))
		case method == http.MethodDelete:
			require.NoError(t, h.RemoveNIC(c))
		default:
			require.NoError(t, h.SetNICMAC(c))
		}
		return rec
	}

	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, `{"pool_id":"`+uuid.NewString()+`"}`).Code, "card on a pool")
	assert.Equal(t, http.StatusForbidden, call(http.MethodPost, `{"vpc_id":"`+uuid.NewString()+`","mac_address":"52:54:00:12:34:56"}`).Code, "chosen MAC")
	assert.Equal(t, http.StatusForbidden, call(http.MethodPut, `{"mac_address":"52:54:00:12:34:56"}`, "nic_id", uuid.NewString()).Code, "MAC change")
	assert.Equal(t, http.StatusNotFound, call(http.MethodDelete, ``, "nic_id", cardID).Code, "card not on their VPC")
}
