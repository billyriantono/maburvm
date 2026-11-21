package handler

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// pageParams reads page and page_size (default 50, at most 200).
func pageParams(c echo.Context) (page, size int) {
	page, _ = strconv.Atoi(c.QueryParam("page"))
	size, _ = strconv.Atoi(c.QueryParam("page_size"))
	if size <= 0 {
		size = 50
	}
	return max(page, 1), min(size, 200)
}

// IPAllocationEvent is one holding of an address, for the Logs page.
type IPAllocationEvent struct {
	ID         string     `json:"id" gorm:"column:id"`
	Address    string     `json:"address" gorm:"column:address"`
	VMID       *string    `json:"vm_id" gorm:"column:vm_id"`
	VMHostname string     `json:"vm_hostname" gorm:"column:vm_hostname"`
	UserEmail  string     `json:"user_email" gorm:"column:user_email"`
	NodeName   string     `json:"node_name" gorm:"column:node_name"`
	Kind       string     `json:"kind" gorm:"column:kind"`
	AssignedAt time.Time  `json:"assigned_at" gorm:"column:assigned_at"`
	ReleasedAt *time.Time `json:"released_at" gorm:"column:released_at"`
	Estimated  bool       `json:"estimated" gorm:"column:estimated"`
}

// RegisterLogRoutes mounts the admin Logs area (recent API requests, address
// assignments; the email log is under the system settings) and the list of
// hypervisor groups.
func RegisterLogRoutes(e *echo.Echo, db *gorm.DB) {
	g := e.Group("/api/v1/logs", middleware.RequireAuth(db), middleware.RequirePermission(middleware.PermStaff))

	g.GET("/requests", func(c echo.Context) error {
		page, size := pageParams(c)
		minMS, _ := strconv.ParseFloat(c.QueryParam("min_ms"), 64)
		rows, total := middleware.RecentRequests(middleware.RequestFilter{
			Method: c.QueryParam("method"), Status: c.QueryParam("status"), Q: c.QueryParam("q"), MinLatencyMS: minMS,
		}, size, (page-1)*size)
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "meta": PageMeta(page, size, int64(total)), "data": map[string]interface{}{
			"requests": rows, "total": total, "page": page, "page_size": size, "kept": middleware.RequestLogSize,
		}})
	}, middleware.RequirePermission("audit:read"))

	g.GET("/ip-allocations", func(c echo.Context) error {
		page, size := pageParams(c)
		// A fresh chain for the count and for the page: GORM's Count leaves state behind.
		q := db.WithContext(c.Request().Context()).Session(&gorm.Session{}).Table("ip_assignment_history h").
			Joins("LEFT JOIN nodes n ON CAST(n.id AS TEXT) = h.node_id")
		if s := strings.ToLower(strings.TrimSpace(c.QueryParam("q"))); s != "" {
			like := "%" + s + "%"
			q = q.Where("LOWER(host(h.address)) LIKE ? OR LOWER(h.vm_hostname) LIKE ? OR LOWER(h.user_email) LIKE ? OR h.vm_id = ?", like, like, like, s)
		}
		switch c.QueryParam("state") {
		case "held":
			q = q.Where("h.released_at IS NULL")
		case "released":
			q = q.Where("h.released_at IS NOT NULL")
		}
		if k := c.QueryParam("kind"); k != "" {
			q = q.Where("h.kind = ?", k)
		}
		q = q.Session(&gorm.Session{})
		var total int64
		if err := q.Count(&total).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not read the address history"})
		}
		rows := []IPAllocationEvent{}
		if err := q.Select(`h.id, host(h.address) AS address, h.vm_id, h.vm_hostname, h.user_email, COALESCE(n.name, '') AS node_name,
			h.kind, h.assigned_at, h.released_at, h.estimated`).
			Order("COALESCE(h.released_at, h.assigned_at) DESC, h.id").Limit(size).Offset((page - 1) * size).Scan(&rows).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not read the address history"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "meta": PageMeta(page, size, total), "data": map[string]interface{}{
			"events": rows, "total": total, "page": page, "page_size": size,
		}})
	}, middleware.RequirePermission("network:read"))

	// Hypervisor groups in use, for the node and plan forms.
	e.GET("/api/v1/node-groups", func(c echo.Context) error {
		groups, err := service.ListNodeGroups(c.Request().Context(), db)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Could not read the node groups"})
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": groups})
	}, middleware.RequireAuth(db), middleware.RequirePermission(middleware.PermStaff), middleware.RequirePermission("node:read"))
}
