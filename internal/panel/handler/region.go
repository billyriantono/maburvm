package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
	"gorm.io/gorm"
)

// RegionHandler exposes the locations a customer chooses between when ordering.
type RegionHandler struct {
	service *service.RegionService
}

func NewRegionHandler(s *service.RegionService) *RegionHandler { return &RegionHandler{service: s} }

func RegisterRegionRoutes(e *echo.Echo, h *RegionHandler, db *gorm.DB) {
	g := e.Group("/api/v1/regions")
	g.Use(middleware.RequireAuth(db))
	// Every authenticated caller may list regions — a customer cannot order
	// without seeing them, and a location plus its flag reveals nothing sensitive.
	g.GET("", h.List)
	g.POST("", h.Create, middleware.RequirePermission("admin:access"))
	g.PUT("/:id", h.Update, middleware.RequirePermission("admin:access"))
	g.DELETE("/:id", h.Delete, middleware.RequirePermission("admin:access"))
	g.POST("/:id/nodes/:node_id", h.AssignNode, middleware.RequirePermission("admin:access"))
}

// List returns orderable regions for customers and every region for admins, so
// an operator can still see one that is disabled or has no capacity.
func (h *RegionHandler) List(c echo.Context) error {
	availableOnly := true
	if userCtx, ok := middleware.GetUserContext(c); ok && userCtx.Role == models.RoleAdmin {
		availableOnly = c.QueryParam("available") == "true"
	}
	regions, err := h.service.List(c.Request().Context(), availableOnly)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	out := make([]map[string]interface{}, 0, len(regions))
	for i := range regions {
		out = append(out, map[string]interface{}{
			"id": regions[i].ID, "slug": regions[i].Slug, "name": regions[i].Name,
			"country": regions[i].Country,
			// The flag travels with the region so every client renders the same
			// glyph without shipping an icon set or mapping codes itself.
			"flag":       service.CountryFlag(regions[i].Country),
			"enabled":    regions[i].Enabled,
			"node_count": regions[i].NodeCount,
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

func (h *RegionHandler) Create(c echo.Context) error {
	var req service.CreateRegionRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	region, err := h.service.Create(c.Request().Context(), &req)
	if err != nil {
		return badRequest(c, err.Error())
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": region})
}

func (h *RegionHandler) Update(c echo.Context) error {
	var req service.CreateRegionRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	region, err := h.service.Update(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		return regionError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": region})
}

func (h *RegionHandler) Delete(c echo.Context) error {
	if err := h.service.Delete(c.Request().Context(), c.Param("id")); err != nil {
		return regionError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *RegionHandler) AssignNode(c echo.Context) error {
	if err := h.service.AssignNode(c.Request().Context(), c.Param("node_id"), c.Param("id")); err != nil {
		return regionError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

func regionError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrRegionNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "region not found"})
	case errors.Is(err, service.ErrRegionInUse):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
	default:
		return badRequest(c, err.Error())
	}
}
