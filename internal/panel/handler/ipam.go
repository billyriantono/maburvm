package handler

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

type IPAMHandler struct {
	service *service.IPAMService
}

func NewIPAMHandler(service *service.IPAMService) *IPAMHandler {
	return &IPAMHandler{service: service}
}

func RegisterIPAMRoutes(e *echo.Echo, h *IPAMHandler, db *gorm.DB) {
	pools := e.Group("/api/v1/ip-pools")
	pools.Use(panelMiddleware.RequireAuth(db))
	pools.GET("", h.ListPools, panelMiddleware.RequirePermission("network:read"))
	pools.POST("", h.CreatePool, panelMiddleware.RequirePermission("admin:access"))
	pools.GET("/:id", h.GetPool, panelMiddleware.RequirePermission("network:read"))
	pools.PUT("/:id", h.UpdatePool, panelMiddleware.RequirePermission("admin:access"))
	pools.DELETE("/:id", h.DeletePool, panelMiddleware.RequirePermission("admin:access"))
	pools.GET("/:id/addresses", h.ListAddresses, panelMiddleware.RequirePermission("network:read"))
	pools.POST("/:id/addresses", h.AddAddress, panelMiddleware.RequirePermission("admin:access"))
	pools.POST("/:id/allocate", h.AllocateAddress, panelMiddleware.RequirePermission("admin:access"))
	pools.POST("/:id/generate", h.GenerateAddresses, panelMiddleware.RequirePermission("admin:access"))
	pools.POST("/addresses/:address_id/release", h.ReleaseAddress, panelMiddleware.RequirePermission("admin:access"))
	pools.PUT("/addresses/:address_id/rdns", h.SetRDNS, panelMiddleware.RequirePermission("network:update"))
	pools.GET("/:id/rdns-zone", h.GetReverseZone, panelMiddleware.RequirePermission("network:read"))
	pools.POST("/:id/rdns-import", h.ImportRDNS, panelMiddleware.RequirePermission("network:update"))
}

// ImportRDNS pulls existing PTR records from the nameserver into this pool's
// addresses (read-only adoption; does not push back).
func (h *IPAMHandler) ImportRDNS(c echo.Context) error {
	n, err := h.service.ImportRDNS(c.Request().Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrDNSProviderNotConfigured) {
			return badRequest(c, "No live DNS provider configured. Set PDNS_API_URL and PDNS_API_KEY to import existing PTRs.")
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"imported": n}})
}

// SetRDNS sets or clears an address's reverse-DNS (PTR) hostname.
func (h *IPAMHandler) SetRDNS(c echo.Context) error {
	var req struct {
		RDNS string `json:"rdns"`
	}
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	address, err := h.service.SetRDNS(c.Request().Context(), c.Param("address_id"), req.RDNS)
	if err != nil {
		if errors.Is(err, service.ErrIPAddressNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP address not found"})
		}
		return badRequest(c, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": address})
}

// GetReverseZone returns a BIND-style PTR zone fragment for a pool's addresses
// that have an rDNS hostname set (text/plain, downloadable).
func (h *IPAMHandler) GetReverseZone(c echo.Context) error {
	zone, err := h.service.GenerateReverseZone(c.Request().Context(), c.Param("id"))
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(zone))
}

func (h *IPAMHandler) CreatePool(c echo.Context) error {
	var req service.CreateIPPoolRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	pool, err := h.service.CreatePool(c.Request().Context(), &req)
	if err != nil {
		return badRequest(c, err.Error())
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": pool})
}

func (h *IPAMHandler) ListPools(c echo.Context) error {
	limit, offset := pagination(c)
	pools, err := h.service.ListPools(c.Request().Context(), limit, offset)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list IP pools"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": pools})
}

func (h *IPAMHandler) GetPool(c echo.Context) error {
	pool, err := h.service.GetPool(c.Request().Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, service.ErrIPPoolNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": pool})
}

// UpdatePool edits an existing pool's metadata (name, gateway, bridge,
// description, node assignment). Editing the bridge is the path out of a stuck
// VM: the VM picks up the corrected bridge on its next start.
func (h *IPAMHandler) UpdatePool(c echo.Context) error {
	var req service.UpdateIPPoolRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	pool, err := h.service.UpdatePool(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		if errors.Is(err, service.ErrIPPoolNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		}
		return badRequest(c, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": pool})
}

func (h *IPAMHandler) DeletePool(c echo.Context) error {
	if err := h.service.DeletePool(c.Request().Context(), c.Param("id")); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *IPAMHandler) AddAddress(c echo.Context) error {
	var req service.CreateIPAddressRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	address, err := h.service.AddAddress(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		if errors.Is(err, service.ErrIPPoolNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		}
		return badRequest(c, err.Error())
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": address})
}

func (h *IPAMHandler) ListAddresses(c echo.Context) error {
	limit, offset := pagination(c)
	addresses, err := h.service.ListAddresses(c.Request().Context(), c.Param("id"), limit, offset)
	if err != nil {
		if errors.Is(err, service.ErrIPPoolNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": addresses})
}

func (h *IPAMHandler) AllocateAddress(c echo.Context) error {
	var req service.AllocateIPAddressRequest
	if err := c.Bind(&req); err != nil {
		return badRequest(c, "Invalid request body")
	}
	req.PoolID = c.Param("id")
	address, err := h.service.AllocateAddress(c.Request().Context(), &req)
	if err != nil {
		switch {
		case errors.Is(err, service.ErrIPPoolNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP pool not found"})
		case errors.Is(err, service.ErrNoAvailableIPAddress):
			return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": address})
}

func (h *IPAMHandler) ReleaseAddress(c echo.Context) error {
	if err := h.service.ReleaseAddress(c.Request().Context(), c.Param("address_id")); err != nil {
		if errors.Is(err, service.ErrIPAddressNotFound) {
			return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "IP address not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.NoContent(http.StatusNoContent)
}

func pagination(c echo.Context) (int, int) {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	offset, _ := strconv.Atoi(c.QueryParam("offset"))
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

func badRequest(c echo.Context, message string) error {
	return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": message})
}

func (h *IPAMHandler) GenerateAddresses(c echo.Context) error {
	id := c.Param("id")
	count, err := h.service.GenerateAddresses(c.Request().Context(), id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Generated %d addresses", count),
		"data":    map[string]interface{}{"count": count},
	})
}
