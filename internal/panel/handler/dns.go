package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// DNSHandler handles forward DNS zone and record endpoints.
type DNSHandler struct {
	service *service.DNSService
}

// NewDNSHandler creates a new DNSHandler.
func NewDNSHandler(s *service.DNSService) *DNSHandler {
	return &DNSHandler{service: s}
}

func dnsZoneError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrDNSZoneNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "DNS zone not found"})
	case errors.Is(err, service.ErrDNSRecordNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "DNS record not found"})
	case errors.Is(err, service.ErrDNSZoneExists):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "DNS zone already exists"})
	case errors.Is(err, service.ErrInvalidDNSRecord):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	case errors.Is(err, service.ErrDNSProviderNotConfigured):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error": "No live DNS provider configured. Set PDNS_API_URL and PDNS_API_KEY to enable nameserver push.",
		})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
}

// GetProvider reports whether a live nameserver provider is configured.
func (h *DNSHandler) GetProvider(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]interface{}{
		"success": true,
		"data": map[string]interface{}{
			"configured": h.service.ProviderConfigured(),
			"name":       h.service.ProviderName(),
		},
	})
}

// SyncZone pushes a zone's full record set to the live nameserver.
func (h *DNSHandler) SyncZone(c echo.Context) error {
	if err := h.service.SyncZone(c.Request().Context(), c.Param("id")); err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Zone synced to nameserver"})
}

func (h *DNSHandler) ListZones(c echo.Context) error {
	zones, err := h.service.ListZones(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": zones})
}

func (h *DNSHandler) CreateZone(c echo.Context) error {
	var req service.ZoneRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	zone, err := h.service.CreateZone(c.Request().Context(), &req)
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": zone})
}

func (h *DNSHandler) GetZone(c echo.Context) error {
	zone, err := h.service.GetZone(c.Request().Context(), c.Param("id"))
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": zone})
}

func (h *DNSHandler) UpdateZone(c echo.Context) error {
	var req service.ZoneRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	zone, err := h.service.UpdateZone(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": zone})
}

func (h *DNSHandler) DeleteZone(c echo.Context) error {
	if err := h.service.DeleteZone(c.Request().Context(), c.Param("id")); err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "DNS zone deleted"})
}

func (h *DNSHandler) ListRecords(c echo.Context) error {
	records, err := h.service.ListRecords(c.Request().Context(), c.Param("id"))
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": records})
}

func (h *DNSHandler) CreateRecord(c echo.Context) error {
	var req service.RecordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	record, err := h.service.CreateRecord(c.Request().Context(), c.Param("id"), &req)
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": record})
}

func (h *DNSHandler) UpdateRecord(c echo.Context) error {
	var req service.RecordRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	record, err := h.service.UpdateRecord(c.Request().Context(), c.Param("recordId"), &req)
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": record})
}

func (h *DNSHandler) DeleteRecord(c echo.Context) error {
	if err := h.service.DeleteRecord(c.Request().Context(), c.Param("recordId")); err != nil {
		return dnsZoneError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "DNS record deleted"})
}

func (h *DNSHandler) ExportZone(c echo.Context) error {
	zone, err := h.service.ExportZone(c.Request().Context(), c.Param("id"))
	if err != nil {
		return dnsZoneError(c, err)
	}
	return c.Blob(http.StatusOK, "text/plain; charset=utf-8", []byte(zone))
}

// RegisterDNSRoutes registers DNS routes. Reads need network:read; mutations need admin.
func RegisterDNSRoutes(e *echo.Echo, h *DNSHandler, db *gorm.DB) {
	g := e.Group("/api/v1/dns")
	g.Use(panelMiddleware.RequireAuth(db))
	read := panelMiddleware.RequirePermission("network:read")
	admin := panelMiddleware.RequirePermission("admin:access")

	g.GET("/provider", h.GetProvider, read)
	g.GET("/zones", h.ListZones, read)
	g.POST("/zones", h.CreateZone, admin)
	g.GET("/zones/:id", h.GetZone, read)
	g.PUT("/zones/:id", h.UpdateZone, admin)
	g.DELETE("/zones/:id", h.DeleteZone, admin)
	g.GET("/zones/:id/export", h.ExportZone, read)
	g.POST("/zones/:id/sync", h.SyncZone, admin)

	g.GET("/zones/:id/records", h.ListRecords, read)
	g.POST("/zones/:id/records", h.CreateRecord, admin)
	g.PUT("/records/:recordId", h.UpdateRecord, admin)
	g.DELETE("/records/:recordId", h.DeleteRecord, admin)
}
