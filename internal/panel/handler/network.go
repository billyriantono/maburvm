package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// NetworkHandler handles HTTP requests for network management
type NetworkHandler struct {
	service *service.NetworkService
}

// NewNetworkHandler creates a new NetworkHandler instance
func NewNetworkHandler(service *service.NetworkService) *NetworkHandler {
	return &NetworkHandler{
		service: service,
	}
}

// AddNetworkRequest represents a request to add a network interface
type AddNetworkRequest struct {
	IPAddress      string `json:"ip_address" validate:"required,ip"`
	BandwidthLimit int64  `json:"bandwidth_limit,omitempty" validate:"omitempty,min=0"`
	VLANID         *int   `json:"vlan_id,omitempty" validate:"omitempty,min=1,max=4094"`
}

// NetworkResponse represents a network interface in the response
type NetworkResponse struct {
	ID             string `json:"id"`
	VMID           string `json:"vm_id"`
	IPAddress      string `json:"ip_address"`
	BandwidthLimit int64  `json:"bandwidth_limit"`
	VLANID         *int   `json:"vlan_id,omitempty"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

// AddNetworkInterface handles POST /api/vms/:id/networks - Add network interface to VM
func (h *NetworkHandler) AddNetworkInterface(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req AddNetworkRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	// Validate request
	if req.IPAddress == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "IP address is required",
		})
	}

	resp, err := h.service.AddNetworkInterface(c.Request().Context(), vmID, &service.AddNetworkRequest{
		IPAddress:      req.IPAddress,
		BandwidthLimit: req.BandwidthLimit,
		VLANID:         req.VLANID,
	})

	if err != nil {
		switch {
		case errors.Is(err, service.ErrIPAlreadyInUse):
			return c.JSON(http.StatusConflict, map[string]interface{}{
				"error":   "Conflict",
				"message": err.Error(),
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Network interface added successfully",
		"data": NetworkResponse{
			ID:             resp.Network.ID,
			VMID:           resp.Network.VMID,
			IPAddress:      resp.Network.IPAddress,
			BandwidthLimit: resp.Network.BandwidthLimit,
			VLANID:         resp.Network.VLANID,
			CreatedAt:      resp.Network.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:      resp.Network.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// SetBandwidthRequest represents a request to set bandwidth limit
type SetBandwidthRequest struct {
	BandwidthLimit int64 `json:"bandwidth_limit" validate:"required,min=0"`
}

// SetBandwidthLimit handles PUT /api/vms/:id/networks/:network_id/bandwidth - Set bandwidth limit
func (h *NetworkHandler) SetBandwidthLimit(c echo.Context) error {
	vmID := c.Param("id")
	networkID := c.Param("network_id")

	if vmID == "" || networkID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Network ID are required",
		})
	}

	var req SetBandwidthRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if err := h.service.SetBandwidthLimit(c.Request().Context(), vmID, networkID, &service.SetBandwidthRequest{
		BandwidthLimit: req.BandwidthLimit,
	}); err != nil {
		switch {
		case errors.Is(err, service.ErrNetworkNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Network not found",
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Bandwidth limit set successfully",
	})
}

// AddPortForwardRequest represents a request to add a port forward rule
type AddPortForwardRequest struct {
	ExternalPort int    `json:"external_port" validate:"required,min=1,max=65535"`
	InternalPort int    `json:"internal_port" validate:"required,min=1,max=65535"`
	Protocol     string `json:"protocol,omitempty" validate:"omitempty,oneof=tcp udp"`
	SourceIP     string `json:"source_ip,omitempty" validate:"omitempty,ip_or_cidr"`
}

// PortForwardResponse represents a port forward rule in the response
type PortForwardResponse struct {
	ID           string `json:"id"`
	VMID         string `json:"vm_id"`
	NetworkID    string `json:"network_id"`
	ExternalPort int    `json:"external_port"`
	InternalPort int    `json:"internal_port"`
	Protocol     string `json:"protocol"`
	SourceIP     string `json:"source_ip"`
	CreatedAt    string `json:"created_at"`
}

// AddPortForward handles POST /api/vms/:id/networks/:network_id/port-forwards - Add NAT rule
func (h *NetworkHandler) AddPortForward(c echo.Context) error {
	vmID := c.Param("id")
	networkID := c.Param("network_id")

	if vmID == "" || networkID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Network ID are required",
		})
	}

	var req AddPortForwardRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if req.ExternalPort == 0 || req.InternalPort == 0 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "External port and internal port are required",
		})
	}

	resp, err := h.service.AddPortForward(c.Request().Context(), vmID, networkID, &service.AddPortForwardRequest{
		ExternalPort: req.ExternalPort,
		InternalPort: req.InternalPort,
		Protocol:     req.Protocol,
		SourceIP:     req.SourceIP,
	})

	if err != nil {
		switch {
		case errors.Is(err, service.ErrNetworkNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Network not found",
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Port forward rule added successfully",
		"data": PortForwardResponse{
			ID:           resp.PortForward.ID,
			VMID:         resp.PortForward.VMID,
			NetworkID:    resp.PortForward.NetworkID,
			ExternalPort: resp.PortForward.ExternalPort,
			InternalPort: resp.PortForward.InternalPort,
			Protocol:     resp.PortForward.Protocol,
			SourceIP:     resp.PortForward.SourceIP,
			CreatedAt:    resp.PortForward.CreatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// RemovePortForward handles DELETE /api/vms/:id/networks/:network_id/port-forwards/:forward_id - Remove NAT
func (h *NetworkHandler) RemovePortForward(c echo.Context) error {
	vmID := c.Param("id")
	networkID := c.Param("network_id")
	forwardID := c.Param("forward_id")

	if vmID == "" || networkID == "" || forwardID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID, Network ID, and Forward ID are required",
		})
	}

	if err := h.service.RemovePortForward(c.Request().Context(), vmID, networkID, forwardID); err != nil {
		switch {
		case errors.Is(err, service.ErrPortForwardNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Port forward not found",
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Port forward rule removed successfully",
	})
}

// AddFirewallRuleRequest represents a request to add a firewall rule
type AddFirewallRuleRequest struct {
	Protocol  string `json:"protocol" validate:"required,oneof=tcp udp icmp all"`
	PortRange string `json:"port_range,omitempty" validate:"omitempty,port_range"`
	Action    string `json:"action" validate:"required,oneof=allow deny"`
	Direction string `json:"direction" validate:"required,oneof=inbound outbound"`
	SourceIP  string `json:"source_ip,omitempty" validate:"omitempty,ip_or_cidr"`
	Priority  int    `json:"priority" validate:"required,min=1,max=1000"`
}

// FirewallRuleResponse represents a firewall rule in the response
type FirewallRuleResponse struct {
	ID        string `json:"id"`
	VMID      string `json:"vm_id"`
	Protocol  string `json:"protocol"`
	PortRange string `json:"port_range,omitempty"`
	Action    string `json:"action"`
	Direction string `json:"direction"`
	SourceIP  string `json:"source_ip"`
	Priority  int    `json:"priority"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// AddFirewallRule handles POST /api/vms/:id/firewall-rules - Add firewall rule
func (h *NetworkHandler) AddFirewallRule(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	var req AddFirewallRuleRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if req.Protocol == "" || req.Action == "" || req.Direction == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Protocol, action, and direction are required",
		})
	}

	resp, err := h.service.AddFirewallRule(c.Request().Context(), vmID, &service.AddFirewallRuleRequest{
		Protocol:  req.Protocol,
		PortRange: req.PortRange,
		Action:    req.Action,
		Direction: req.Direction,
		SourceIP:  req.SourceIP,
		Priority:  req.Priority,
	})

	if err != nil {
		switch {
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusCreated, map[string]interface{}{
		"message": "Firewall rule added successfully",
		"data": FirewallRuleResponse{
			ID:        resp.FirewallRule.ID,
			VMID:      resp.FirewallRule.VMID,
			Protocol:  resp.FirewallRule.Protocol,
			PortRange: resp.FirewallRule.PortRange,
			Action:    resp.FirewallRule.Action,
			Direction: resp.FirewallRule.Direction,
			SourceIP:  resp.FirewallRule.SourceIP,
			Priority:  resp.FirewallRule.Priority,
			CreatedAt: resp.FirewallRule.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: resp.FirewallRule.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		},
	})
}

// RemoveFirewallRule handles DELETE /api/vms/:id/firewall-rules/:rule_id - Remove firewall rule
func (h *NetworkHandler) RemoveFirewallRule(c echo.Context) error {
	vmID := c.Param("id")
	ruleID := c.Param("rule_id")

	if vmID == "" || ruleID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Rule ID are required",
		})
	}

	if err := h.service.RemoveFirewallRule(c.Request().Context(), vmID, ruleID); err != nil {
		switch {
		case errors.Is(err, service.ErrFirewallRuleNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Firewall rule not found",
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Firewall rule removed successfully",
	})
}

// SetVLANRequest represents a request to set VLAN ID
type SetVLANRequest struct {
	VLANID int `json:"vlan_id" validate:"required,min=1,max=4094"`
}

// SetVLAN handles PUT /api/vms/:id/networks/:network_id/vlan - Set VLAN ID
func (h *NetworkHandler) SetVLAN(c echo.Context) error {
	vmID := c.Param("id")
	networkID := c.Param("network_id")

	if vmID == "" || networkID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Network ID are required",
		})
	}

	var req SetVLANRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "Invalid request body",
		})
	}

	if err := h.service.SetVLAN(c.Request().Context(), vmID, networkID, &service.SetVLANRequest{
		VLANID: req.VLANID,
	}); err != nil {
		switch {
		case errors.Is(err, service.ErrNetworkNotFound):
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "Network not found",
			})
		case err.Error() == "VM not found":
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		default:
			return c.JSON(http.StatusInternalServerError, map[string]interface{}{
				"error":   "Internal Server Error",
				"message": err.Error(),
			})
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "VLAN ID set successfully",
	})
}

// ListNetworkInterfaces handles GET /api/vms/:id/networks - List network interfaces
func (h *NetworkHandler) ListNetworkInterfaces(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	networks, err := h.service.GetNetworkInterfaces(c.Request().Context(), vmID)
	if err != nil {
		if err.Error() == "VM not found" {
			return c.JSON(http.StatusNotFound, map[string]interface{}{
				"error":   "Not Found",
				"message": "VM not found",
			})
		}
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	response := make([]NetworkResponse, len(networks))
	for i, net := range networks {
		response[i] = NetworkResponse{
			ID:             net.ID,
			VMID:           net.VMID,
			IPAddress:      net.IPAddress,
			BandwidthLimit: net.BandwidthLimit,
			VLANID:         net.VLANID,
			CreatedAt:      net.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt:      net.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Network interfaces retrieved successfully",
		"data":    response,
	})
}

// ListPortForwards handles GET /api/vms/:id/networks/:network_id/port-forwards - List port forwards
func (h *NetworkHandler) ListPortForwards(c echo.Context) error {
	vmID := c.Param("id")
	networkID := c.Param("network_id")

	if vmID == "" || networkID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID and Network ID are required",
		})
	}

	portForwards, err := h.service.GetPortForwards(c.Request().Context(), vmID, networkID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	response := make([]PortForwardResponse, len(portForwards))
	for i, pf := range portForwards {
		response[i] = PortForwardResponse{
			ID:           pf.ID,
			VMID:         pf.VMID,
			NetworkID:    pf.NetworkID,
			ExternalPort: pf.ExternalPort,
			InternalPort: pf.InternalPort,
			Protocol:     pf.Protocol,
			SourceIP:     pf.SourceIP,
			CreatedAt:    pf.CreatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Port forwards retrieved successfully",
		"data":    response,
	})
}

// ListFirewallRules handles GET /api/vms/:id/firewall-rules - List firewall rules
func (h *NetworkHandler) ListFirewallRules(c echo.Context) error {
	vmID := c.Param("id")
	if vmID == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{
			"error":   "Bad Request",
			"message": "VM ID is required",
		})
	}

	rules, err := h.service.GetFirewallRules(c.Request().Context(), vmID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{
			"error":   "Internal Server Error",
			"message": err.Error(),
		})
	}

	response := make([]FirewallRuleResponse, len(rules))
	for i, rule := range rules {
		response[i] = FirewallRuleResponse{
			ID:        rule.ID,
			VMID:      rule.VMID,
			Protocol:  rule.Protocol,
			PortRange: rule.PortRange,
			Action:    rule.Action,
			Direction: rule.Direction,
			SourceIP:  rule.SourceIP,
			Priority:  rule.Priority,
			CreatedAt: rule.CreatedAt.Format("2006-01-02T15:04:05Z"),
			UpdatedAt: rule.UpdatedAt.Format("2006-01-02T15:04:05Z"),
		}
	}

	return c.JSON(http.StatusOK, map[string]interface{}{
		"message": "Firewall rules retrieved successfully",
		"data":    response,
	})
}

// RegisterNetworkRoutes registers all network routes with the Echo router
func RegisterNetworkRoutes(e *echo.Echo, handler *NetworkHandler, db interface{}) {
	// Create VM routes group
	vms := e.Group("/api/v1/vms")

	// Apply authentication middleware
	vms.Use(middleware.RequireAuth(nil))

	// Network interface routes
	vms.POST("/:id/networks", handler.AddNetworkInterface, middleware.RequirePermission("vm:update"))
	vms.GET("/:id/networks", handler.ListNetworkInterfaces, middleware.RequirePermission("vm:read"))

	// Bandwidth routes
	vms.PUT("/:id/networks/:network_id/bandwidth", handler.SetBandwidthLimit, middleware.RequirePermission("vm:update"))

	// Port forward routes
	vms.POST("/:id/networks/:network_id/port-forwards", handler.AddPortForward, middleware.RequirePermission("vm:update"))
	vms.GET("/:id/networks/:network_id/port-forwards", handler.ListPortForwards, middleware.RequirePermission("vm:read"))
	vms.DELETE("/:id/networks/:network_id/port-forwards/:forward_id", handler.RemovePortForward, middleware.RequirePermission("vm:update"))

	// VLAN routes
	vms.PUT("/:id/networks/:network_id/vlan", handler.SetVLAN, middleware.RequirePermission("vm:update"))

	// Firewall routes
	vms.POST("/:id/firewall-rules", handler.AddFirewallRule, middleware.RequirePermission("vm:update"))
	vms.GET("/:id/firewall-rules", handler.ListFirewallRules, middleware.RequirePermission("vm:read"))
	vms.DELETE("/:id/firewall-rules/:rule_id", handler.RemoveFirewallRule, middleware.RequirePermission("vm:update"))
}
