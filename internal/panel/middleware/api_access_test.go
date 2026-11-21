package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The "Enable API access" switch decides whether an API key is even looked at.
func TestEnableAPISwitchGatesAPIKeys(t *testing.T) {
	db := setupAuthTestDB(t)
	require.NoError(t, db.Exec(`CREATE TABLE IF NOT EXISTS system_settings (section TEXT PRIMARY KEY, data TEXT)`).Error)
	t.Cleanup(func() { db.Exec(`DROP TABLE system_settings`) })

	call := func() *httptest.ResponseRecorder {
		e := echo.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/vms", nil)
		req.Header.Set("X-API-Key", "mvk_not_a_real_key")
		rec := httptest.NewRecorder()
		h := RequireAuth(db)(func(c echo.Context) error { return c.NoContent(http.StatusOK) })
		require.NoError(t, h(e.NewContext(req, rec)))
		return rec
	}
	set := func(data string) {
		require.NoError(t, db.Exec(`DELETE FROM system_settings`).Error)
		if data != "" {
			require.NoError(t, db.Exec(`INSERT INTO system_settings (section, data) VALUES ('api', ?)`, data).Error)
		}
	}

	for name, data := range map[string]string{
		"never saved":          "",
		"saved without switch": `{"webhookUrl":""}`,
		"switched on":          `{"enableApi":true}`,
	} {
		set(data)
		assert.True(t, APIAccessEnabled(db), name)
		assert.Equal(t, http.StatusUnauthorized, call().Code, "%s: the key is checked (and this one is not real)", name)
	}

	set(`{"enableApi":false}`)
	assert.False(t, APIAccessEnabled(db))
	rec := call()
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "API access is turned off")
}
