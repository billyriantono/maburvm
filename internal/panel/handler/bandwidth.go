package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/authz"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// BandwidthHandler handles HTTP requests for bandwidth usage
type BandwidthHandler struct {
	service *service.BandwidthService
	authz   *authz.Authorizer
}

// NewBandwidthHandler creates a new BandwidthHandler
func NewBandwidthHandler(service *service.BandwidthService, authorizer *authz.Authorizer) *BandwidthHandler {
	return &BandwidthHandler{service: service, authz: authorizer}
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
	// Counts is which direction counts toward the allowance (both, out, in)
	// and ResetDay the day of the month it starts again. Pooled says the
	// allowance is shared by the account's VMs, PoolUsedGB and PoolQuotaGB
	// being the pool's. Current period only.
	Counts      string  `json:"counts,omitempty"`
	ResetDay    int     `json:"reset_day,omitempty"`
	Pooled      bool    `json:"pooled,omitempty"`
	PoolUsedGB  float64 `json:"pool_used_gb,omitempty"`
	PoolQuotaGB float64 `json:"pool_quota_gb,omitempty"`
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

	// Enforce tenant isolation before read.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
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
		t := usage.BlockedAt.UTC().Format("2006-01-02T15:04:05Z")
		resp.BlockedAt = &t
	}
	ctx := c.Request().Context()
	resp.ResetDay, resp.Counts = h.service.TransferTerms(ctx, vmID)
	if pool := h.service.Pool(ctx, vmID); pool.Pooled {
		const gb = 1024 * 1024 * 1024
		resp.Pooled, resp.PoolUsedGB, resp.PoolQuotaGB = true, float64(pool.UsedBytes)/gb, float64(pool.QuotaBytes)/gb
	}

	return c.JSON(http.StatusOK, resp)
}

// RegisterAccountTransferRoutes mounts an account's transfer settings: its
// own reset day and pooled transfer. Staff only.
func RegisterAccountTransferRoutes(e *echo.Echo, db *gorm.DB) {
	auth := middleware.RequireAuth(db)
	e.GET("/api/v1/users/:id/transfer", func(c echo.Context) error {
		t, err := service.LoadAccountTransfer(c.Request().Context(), db, c.Param("id"))
		if err != nil {
			return accountTransferError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "data": t})
	}, auth, middleware.RequirePermission("user:read"))
	e.PUT("/api/v1/users/:id/transfer", func(c echo.Context) error {
		var in service.AccountTransfer
		if err := c.Bind(&in); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": "invalid request body"})
		}
		if err := service.SaveAccountTransfer(c.Request().Context(), db, c.Param("id"), in); err != nil {
			return accountTransferError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"success": true, "data": in})
	}, auth, middleware.RequirePermission("user:update"))
}

func accountTransferError(c echo.Context, err error) error {
	if errors.Is(err, service.ErrUserNotFound) {
		return c.JSON(http.StatusNotFound, map[string]any{"error": "Not Found", "message": "User not found"})
	}
	return c.JSON(http.StatusBadRequest, map[string]any{"error": "Bad Request", "message": err.Error()})
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

	// Enforce tenant isolation before read.
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
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
			t := usage.BlockedAt.UTC().Format("2006-01-02T15:04:05Z")
			item.BlockedAt = &t
		}
		resp = append(resp, item)
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"vm_id":   vmID,
		"history": resp,
	})
}

// SetVMBandwidthQuota handles PUT /api/v1/vms/:id/bandwidth/quota
// Sets the VM's monthly bandwidth quota in GB (0 = unlimited).
func (h *BandwidthHandler) SetVMBandwidthQuota(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	// Quota management is an admin-only boundary. Reuse current admin semantics;
	// non-admins get 403, missing auth gets 401.
	if !h.authz.AuthorizeAdmin(c) {
		return nil
	}

	var req struct {
		QuotaGB int64 `json:"quota_gb"`
	}
	if err := c.Bind(&req); err != nil || req.QuotaGB < 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "quota_gb must be a non-negative integer (0 = unlimited)",
		})
	}
	if err := h.service.SetVMQuota(c.Request().Context(), vmID, req.QuotaGB); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Bandwidth quota updated",
		"data":    map[string]interface{}{"quota_gb": req.QuotaGB},
	})
}

// RegisterBandwidthRoutes registers bandwidth usage routes
func RegisterBandwidthRoutes(e *echo.Echo, h *BandwidthHandler, db *gorm.DB) {
	bwGroup := e.Group("/api/v1/vms/:id/bandwidth")
	bwGroup.Use(middleware.RequireAuth(db))

	bwGroup.GET("", h.GetVMBandwidth, middleware.RequirePermission("vm:read"))
	bwGroup.GET("/history", h.GetVMBandwidthHistory, middleware.RequirePermission("vm:read"))
	bwGroup.PUT("/quota", h.SetVMBandwidthQuota, middleware.RequirePermission("vm:update"))
}
