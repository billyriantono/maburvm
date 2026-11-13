package handler

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/shared/models"
)

// SearchVM, SearchUser and SearchNode are the rows the header search returns.
type SearchVM struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	Status   string `json:"status"`
	IP       string `json:"ip_address"`
	Owner    string `json:"owner_email,omitempty"`
}

type SearchUser struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
	Role  string `json:"role"`
}

type SearchNode struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPAddress string `json:"ip_address"`
	Status    string `json:"status"`
}

// likePattern turns what someone typed into a contains-match, with the LIKE
// wildcards in it taken literally.
func likePattern(q string) string {
	q = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q))
	return "%" + q + "%"
}

const searchLimit = 8

// RegisterSearchRoutes adds GET /api/v1/search?q=. One box for the things
// people look up by a fragment they remember: a hostname, an address, an ID,
// an email. A customer only ever gets their own VMs back.
func RegisterSearchRoutes(e *echo.Echo, db *gorm.DB) {
	e.GET("/api/v1/search", searchHandler(db), middleware.RequireAuth(db))
}

func searchHandler(db *gorm.DB) echo.HandlerFunc {
	return func(c echo.Context) error {
		user, ok := middleware.GetUserContext(c)
		if !ok {
			return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
		}
		q := strings.TrimSpace(c.QueryParam("q"))
		out := map[string]interface{}{"vms": []SearchVM{}, "users": []SearchUser{}, "nodes": []SearchNode{}}
		if len([]rune(q)) < 2 {
			return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
		}
		like := likePattern(q)
		ctx := c.Request().Context()
		admin := user.Role == models.RoleAdmin

		// CAST works for both an inet column and plain text, so one query
		// matches an address fragment like "203.0.113".
		vmQuery := db.WithContext(ctx).Table("vms").
			Select("vms.id, vms.hostname, vms.status, COALESCE(MIN(CAST(networks.ip_address AS TEXT)), '') AS ip, COALESCE(MAX(users.email), '') AS owner").
			Joins("LEFT JOIN networks ON networks.vm_id = vms.id AND networks.deleted_at IS NULL").
			Joins("LEFT JOIN users ON users.id = vms.user_id").
			Where("vms.deleted_at IS NULL").
			Where("LOWER(vms.hostname) LIKE ? ESCAPE '\\' OR LOWER(CAST(vms.id AS TEXT)) LIKE ? ESCAPE '\\' OR CAST(networks.ip_address AS TEXT) LIKE ? ESCAPE '\\'", like, like, like).
			Group("vms.id, vms.hostname, vms.status").Order("vms.hostname").Limit(searchLimit)
		if !admin {
			vmQuery = vmQuery.Where("vms.user_id = ?", user.ID.String())
		}
		var vms []SearchVM
		if err := vmQuery.Scan(&vms).Error; err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Search failed"})
		}
		for i := range vms {
			vms[i].IP = strings.TrimSuffix(vms[i].IP, "/32")
			if !admin {
				vms[i].Owner = ""
			}
		}
		if vms != nil {
			out["vms"] = vms
		}

		if admin {
			var users []SearchUser
			if err := db.WithContext(ctx).Table("users").Select("id, email, name, role").
				Where("deleted_at IS NULL").
				Where("LOWER(email) LIKE ? ESCAPE '\\' OR LOWER(name) LIKE ? ESCAPE '\\'", like, like).
				Order("email").Limit(searchLimit).Scan(&users).Error; err == nil && users != nil {
				out["users"] = users
			}
			var nodes []SearchNode
			if err := db.WithContext(ctx).Table("nodes").Select("id, name, CAST(ip_address AS TEXT) AS ip_address, status").
				Where("deleted_at IS NULL").
				Where("LOWER(name) LIKE ? ESCAPE '\\' OR CAST(ip_address AS TEXT) LIKE ? ESCAPE '\\'", like, like).
				Order("name").Limit(searchLimit).Scan(&nodes).Error; err == nil && nodes != nil {
				for i := range nodes {
					nodes[i].IPAddress = strings.TrimSuffix(nodes[i].IPAddress, "/32")
				}
				out["nodes"] = nodes
			}
		}
		return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
	}
}
