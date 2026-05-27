package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// BandwidthHandler handles HTTP requests for bandwidth usage
type BandwidthHandler struct {
	service *service.BandwidthService
}

// NewBandwidthHandler creates a new BandwidthHandler
func NewBandwidthHandler(service *service.BandwidthService) *BandwidthHandler {
	return &BandwidthHandler{service: service}
}

// BandwidthUsageResponse represents the API response for bandwidth usage
type BandwidthUsageResponse struct {
	VMID         string  `json:"vm_id"`
	NodeID       string  `json:"node_id"`
	PeriodStart  string  `json:"period_start"`
	PeriodEnd    string  `json:"period_end"`
	RxBytes      int64   `json:"rx_bytes"`
	TxBytes      int64   `json:"tx_bytes"`
	TotalBytes   int64   `json:"total_bytes"`
	UsedGB       float64 `json:"used_gb"`
	QuotaGB      float64 `json:"quota_gb"`
	UsagePercent float64 `json:"usage_percent"`
	Exceeded     bool    `json:"exceeded"`
	BlockedAt    *string `json:"blocked_at,omitempty"`
}

// GetVMBandwidth handles GET /api/v1/vms/:id/bandwidth
// Returns current period bandwidth usage for a VM
func (h *BandwidthHandler) GetVMBandwidth(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	nodeID := c.QueryParam("node_id")

	usage, err := h.service.GetVMUsage(c.Request().Context(), vmID, nodeID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	resp := BandwidthUsageResponse{
		VMID:         usage.VMID,
		NodeID:       usage.NodeID,
		PeriodStart:  usage.PeriodStart.Format("2006-01-02"),
		PeriodEnd:    usage.PeriodEnd.Format("2006-01-02"),
		RxBytes:      usage.RxBytes,
		TxBytes:      usage.TxBytes,
		TotalBytes:   usage.TotalBytes,
		UsedGB:       usage.UsedGB(),
		QuotaGB:      usage.QuotaGB(),
		UsagePercent: usage.UsagePercent(),
		Exceeded:     usage.Exceeded,
	}

	if usage.BlockedAt != nil {
		t := usage.BlockedAt.Format("2006-01-02T15:04:05Z")
		resp.BlockedAt = &t
	}

	return c.JSON(http.StatusOK, resp)
}

// GetVMBandwidthHistory handles GET /api/v1/vms/:id/bandwidth/history
// Returns all billing periods for a VM
func (h *BandwidthHandler) GetVMBandwidthHistory(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	usages, err := h.service.GetVMUsageHistory(c.Request().Context(), vmID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	var resp []BandwidthUsageResponse
	for _, usage := range usages {
		item := BandwidthUsageResponse{
			VMID:         usage.VMID,
			NodeID:       usage.NodeID,
			PeriodStart:  usage.PeriodStart.Format("2006-01-02"),
			PeriodEnd:    usage.PeriodEnd.Format("2006-01-02"),
			RxBytes:      usage.RxBytes,
			TxBytes:      usage.TxBytes,
			TotalBytes:   usage.TotalBytes,
			UsedGB:       usage.UsedGB(),
			QuotaGB:      usage.QuotaGB(),
			UsagePercent: usage.UsagePercent(),
			Exceeded:     usage.Exceeded,
		}
		if usage.BlockedAt != nil {
			t := usage.BlockedAt.Format("2006-01-02T15:04:05Z")
			item.BlockedAt = &t
		}
		resp = append(resp, item)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"vm_id":   vmID,
		"history": resp,
	})
}

// RegisterBandwidthRoutes registers bandwidth usage routes
func RegisterBandwidthRoutes(e *echo.Echo, h *BandwidthHandler, db *gorm.DB) {
	bwGroup := e.Group("/api/v1/vms/:id/bandwidth")
	bwGroup.Use(middleware.RequireAuth(db))

	bwGroup.GET("", h.GetVMBandwidth)
	bwGroup.GET("/history", h.GetVMBandwidthHistory)
}
