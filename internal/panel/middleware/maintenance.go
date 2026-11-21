package middleware

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/shared/models"
)

// MaintenanceSection is the system_settings row holding maintenance mode.
const MaintenanceSection = "maintenance"

// Maintenance is panel maintenance mode: while Enabled, customers and the
// billing system cannot change anything (reads still work) and a banner says
// why. HoldJobs also pauses the job queue, so an upgrade can wait for running
// jobs to finish without new ones starting.
type Maintenance struct {
	Enabled  bool       `json:"enabled"`
	Message  string     `json:"message"`
	Since    *time.Time `json:"since,omitempty"`
	By       string     `json:"by,omitempty"`
	HoldJobs bool       `json:"hold_jobs"`
}

// DefaultMaintenanceNotice is the banner when the Owner wrote none.
const DefaultMaintenanceNotice = "The panel is down for maintenance. Changes are paused for now; your servers keep running."

// Notice is the sentence customers are shown.
func (m Maintenance) Notice() string {
	if m.Message != "" {
		return m.Message
	}
	return DefaultMaintenanceNotice
}

// maintenanceTTL is how long a read is reused. Short, so a switch made by the
// command-line tool (another process) applies within seconds.
const maintenanceTTL = 3 * time.Second

var maintenanceCache struct {
	sync.Mutex
	at time.Time
	v  Maintenance
}

// ReadMaintenance reads maintenance mode from the database. No row, no
// table, or an unreadable row all read as off: the switch fails open, it only
// pauses changes.
func ReadMaintenance(db *gorm.DB) Maintenance {
	var m Maintenance
	if db == nil {
		return m
	}
	var raw string
	if err := db.Raw("SELECT CAST(data AS TEXT) FROM system_settings WHERE section = ?", MaintenanceSection).Scan(&raw).Error; err != nil || raw == "" {
		return m
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return Maintenance{}
	}
	return m
}

// LoadMaintenance is maintenance mode now, read at most every few seconds.
func LoadMaintenance(db *gorm.DB) Maintenance {
	maintenanceCache.Lock()
	defer maintenanceCache.Unlock()
	if !maintenanceCache.at.IsZero() && time.Since(maintenanceCache.at) < maintenanceTTL {
		return maintenanceCache.v
	}
	maintenanceCache.v, maintenanceCache.at = ReadMaintenance(db), time.Now()
	return maintenanceCache.v
}

// RememberMaintenance puts a state just saved in this process into use at once.
func RememberMaintenance(m Maintenance) {
	maintenanceCache.Lock()
	maintenanceCache.v, maintenanceCache.at = m, time.Now()
	maintenanceCache.Unlock()
}

// ForgetMaintenance drops the cached state, so the next request reads it.
func ForgetMaintenance() {
	maintenanceCache.Lock()
	maintenanceCache.at = time.Time{}
	maintenanceCache.Unlock()
}

// RespondMaintenance answers a change refused during maintenance: 503 with a
// Retry-After, which API clients and the billing module treat as "try later".
func RespondMaintenance(c echo.Context, m Maintenance) error {
	c.Response().Header().Set("Retry-After", "300")
	return c.JSON(http.StatusServiceUnavailable, map[string]interface{}{
		"error": "Service Unavailable", "code": "maintenance", "message": m.Notice(),
	})
}

// refuseInMaintenance stops a customer's change while the panel is in
// maintenance. Staff (and staff viewing as a customer) carry on; reads and
// signing out always work.
func refuseInMaintenance(c echo.Context, db *gorm.DB, u *UserContext) (bool, error) {
	if u == nil || u.Role == models.RoleAdmin || u.ImpersonatorID != "" {
		return false, nil
	}
	switch c.Request().Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false, nil
	}
	// The MCP endpoint's POST only carries tool calls; each one is served as
	// its own request and refused there when it changes something.
	if strings.HasSuffix(c.Request().URL.Path, "/auth/logout") || c.Get(allowReadOnlyKeyCtx) != nil {
		return false, nil
	}
	m := LoadMaintenance(db)
	if !m.Enabled {
		return false, nil
	}
	return true, RespondMaintenance(c, m)
}
