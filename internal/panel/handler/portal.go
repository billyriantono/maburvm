package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

type portalHandler struct {
	db       *gorm.DB
	az       *authz.Authorizer
	requests *service.IPRequestService
}

func (h *portalHandler) portal(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": service.LoadCustomerPortal(c.Request().Context(), h.db)})
}

func ipRequestError(c echo.Context, err error) error {
	if refusedByAccount(c, err) {
		return nil
	}
	switch {
	case errors.Is(err, service.ErrIPRequestsOff):
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "code": "ip_requests_off", "message": err.Error()})
	case errors.Is(err, service.ErrIPRequestNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrIPRequestOpen), errors.Is(err, service.ErrIPRequestDecided), errors.Is(err, service.ErrVMAlreadyHasIPv6Prefix):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	default:
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	}
}

func (h *portalHandler) listVMRequests(c echo.Context) error {
	id := c.Param("id")
	if !h.az.AuthorizeVM(c, id) {
		return nil
	}
	rows, err := h.requests.ListForVM(c.Request().Context(), id)
	if err != nil {
		return ipRequestError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": rows})
}

func (h *portalHandler) requestIP(c echo.Context) error {
	id := c.Param("id")
	if !h.az.AuthorizeVM(c, id) {
		return nil
	}
	var req struct {
		Family string `json:"family"`
		Reason string `json:"reason"`
	}
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	var vm models.VM
	if err := h.db.WithContext(c.Request().Context()).First(&vm, "id = ?", id).Error; err != nil {
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": "VM not found"})
	}
	u, _ := middleware.GetUserContext(c)
	r, err := h.requests.Request(c.Request().Context(), &vm, u.ID.String(), req.Family, req.Reason)
	if r == nil {
		return ipRequestError(c, err)
	}
	out := map[string]interface{}{"success": true, "data": r}
	if err != nil { // an automatic grant that has to wait for staff
		out["warning"] = r.Note
	}
	return c.JSON(http.StatusCreated, out)
}

func (h *portalHandler) cancelRequest(c echo.Context) error {
	id := c.Param("id")
	if !h.az.AuthorizeVM(c, id) {
		return nil
	}
	if err := h.requests.Cancel(c.Request().Context(), id, c.Param("request_id")); err != nil {
		return ipRequestError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true})
}

func (h *portalHandler) queue(c echo.Context) error {
	limit, offset := listWindow(c)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, total, err := h.requests.List(c.Request().Context(), c.QueryParam("status"), limit, offset)
	if err != nil {
		return ipRequestError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "meta": OffsetPageMeta(limit, offset, total), "data": rows, "total": total, "limit": limit, "offset": offset})
}

func (h *portalHandler) approve(c echo.Context) error {
	u, _ := middleware.GetUserContext(c)
	r, err := h.requests.Approve(c.Request().Context(), c.Param("id"), u.ID.String())
	if err != nil && (r == nil || r.Status != service.IPRequestGranted) {
		return ipRequestError(c, err)
	}
	out := map[string]interface{}{"success": true, "data": r}
	if err != nil {
		out["warning"] = err.Error()
	}
	return c.JSON(http.StatusOK, out)
}

func (h *portalHandler) reject(c echo.Context) error {
	var req struct {
		Note string `json:"note"`
	}
	_ = c.Bind(&req)
	u, _ := middleware.GetUserContext(c)
	r, err := h.requests.Reject(c.Request().Context(), c.Param("id"), u.ID.String(), req.Note)
	if err != nil {
		return ipRequestError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": r})
}

// RegisterPortalRoutes wires the customer area's settings and address
// requests. net must have address allocation wired.
func RegisterPortalRoutes(e *echo.Echo, db *gorm.DB, net *service.NetworkService) {
	h := &portalHandler{db: db, az: authz.NewAuthorizer(db), requests: service.NewIPRequestService(db, net)}
	auth := middleware.RequireAuth(db)

	e.GET("/api/v1/portal", h.portal, auth)

	vms := e.Group("/api/v1/vms/:id/ip-requests", auth)
	vms.GET("", h.listVMRequests, middleware.RequirePermission("vm:read"))
	vms.POST("", h.requestIP, middleware.RequirePermission("vm:update"))
	vms.DELETE("/:request_id", h.cancelRequest, middleware.RequirePermission("vm:update"))

	staff := e.Group("/api/v1/ip-requests", auth)
	staff.GET("", h.queue, middleware.RequirePermission("network:read"))
	staff.POST("/:id/approve", h.approve, middleware.RequirePermission("network:update"))
	staff.POST("/:id/reject", h.reject, middleware.RequirePermission("network:update"))
}
