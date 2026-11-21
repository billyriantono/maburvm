package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// AddNICRequest asks for another network card. Staff name a pool (and may
// choose the MAC); a customer names one of their own VPCs.
type AddNICRequest struct {
	PoolID     string `json:"pool_id,omitempty"`
	VPCID      string `json:"vpc_id,omitempty"`
	MACAddress string `json:"mac_address,omitempty"`
}

// SetNICMACRequest gives a card another MAC.
type SetNICMACRequest struct {
	MACAddress string `json:"mac_address"`
}

// SetIPv6AddressesRequest replaces the further addresses in a VM's prefix.
type SetIPv6AddressesRequest struct {
	Addresses []string `json:"addresses"`
}

func nicError(c echo.Context, err error) error {
	var allowance *service.AllowanceError
	switch {
	case errors.As(err, &allowance):
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": allowance.Message})
	case errors.Is(err, service.ErrVMNotFound), errors.Is(err, service.ErrNICNotFound), errors.Is(err, service.ErrVPCNotFound),
		errors.Is(err, service.ErrIPPoolNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Not Found", "message": err.Error()})
	case errors.Is(err, service.ErrInvalidMAC), errors.Is(err, service.ErrNICPoolInvalid), errors.Is(err, service.ErrInvalidPolicy),
		errors.Is(err, service.ErrNICFirst), errors.Is(err, service.ErrNoRoutedIPv6):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": err.Error()})
	case errors.Is(err, service.ErrMACInUse), errors.Is(err, service.ErrNICLimit), errors.Is(err, service.ErrNICSameBridge),
		errors.Is(err, service.ErrNICNeedsStopped), errors.Is(err, service.ErrNICsUnsupported), errors.Is(err, service.ErrIPv6ExtraUnsupported),
		errors.Is(err, service.ErrVMMigrating), errors.Is(err, service.ErrNoAvailableIPAddress):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": "Conflict", "message": err.Error()})
	}
	return c.JSON(http.StatusBadGateway, map[string]interface{}{"error": "Bad Gateway", "message": err.Error()})
}

// ListNICs handles GET /api/v1/vms/:id/nics.
func (h *NetworkHandler) ListNICs(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	nics, err := h.service.ListNICs(c.Request().Context(), vmID)
	if err != nil {
		return nicError(c, err)
	}
	if isClientRole(c) {
		for i := range nics {
			nics[i].Bridge = "" // host topology; the network's name says enough
		}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nics})
}

// AddNIC handles POST /api/v1/vms/:id/nics.
func (h *NetworkHandler) AddNIC(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	var req AddNICRequest
	if err := c.Bind(&req); err != nil || (req.PoolID == "") == (req.VPCID == "") {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "give either pool_id or vpc_id"})
	}
	ctx := c.Request().Context()
	var (
		nic  *service.VMNICView
		note string
		err  error
	)
	if isClientRole(c) {
		// A customer joins their own private networks only.
		if req.PoolID != "" || req.MACAddress != "" {
			return clientNetworkSelectionDenied(c)
		}
		vm, verr := h.service.VMForCard(ctx, vmID)
		if verr != nil {
			return nicError(c, verr)
		}
		nic, note, err = h.service.AddVPCNIC(ctx, vm, vm.UserID, req.VPCID)
	} else if req.VPCID != "" {
		vm, verr := h.service.VMForCard(ctx, vmID)
		if verr != nil {
			return nicError(c, verr)
		}
		nic, note, err = h.service.AddVPCNIC(ctx, vm, vm.UserID, req.VPCID)
	} else {
		nic, note, err = h.service.AddNIC(ctx, vmID, req.PoolID, req.MACAddress)
	}
	if err != nil {
		return nicError(c, err)
	}
	if isClientRole(c) {
		nic.Bridge = ""
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": nic, "message": note})
}

// RemoveNIC handles DELETE /api/v1/vms/:id/nics/:nic_id. A customer removes
// only a card on one of their own private networks.
func (h *NetworkHandler) RemoveNIC(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	ctx := c.Request().Context()
	if isClientRole(c) && !h.service.CardOnOwnVPC(ctx, vmID, c.Param("nic_id")) {
		// Only a card on one of their own private networks is theirs to remove.
		return nicError(c, service.ErrNICNotFound)
	}
	note, err := h.service.RemoveNIC(ctx, vmID, c.Param("nic_id"))
	if err != nil {
		return nicError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": note})
}

// SetNICMAC handles PUT /api/v1/vms/:id/nics/:nic_id/mac. Staff only.
func (h *NetworkHandler) SetNICMAC(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	if userCtx, ok := middleware.GetUserContext(c); !ok || userCtx.Role != models.RoleAdmin {
		return c.JSON(http.StatusForbidden, map[string]interface{}{"error": "Forbidden", "message": "Only staff can change a card's MAC address."})
	}
	var req SetNICMACRequest
	if err := c.Bind(&req); err != nil || req.MACAddress == "" {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "mac_address is required"})
	}
	nic, err := h.service.SetNICMAC(c.Request().Context(), vmID, c.Param("nic_id"), req.MACAddress)
	if err != nil {
		return nicError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nic})
}

// GetIPv6Addresses handles GET /api/v1/vms/:id/ipv6/addresses.
func (h *NetworkHandler) GetIPv6Addresses(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	out, err := h.service.GetIPv6Addresses(c.Request().Context(), vmID)
	if err != nil {
		return nicError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

// SetIPv6Addresses handles PUT /api/v1/vms/:id/ipv6/addresses {addresses}.
func (h *NetworkHandler) SetIPv6Addresses(c echo.Context) error {
	vmID := c.Param("id")
	if !h.authz.AuthorizeVM(c, vmID) {
		return nil
	}
	var req SetIPv6AddressesRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	out, err := h.service.SetIPv6Addresses(c.Request().Context(), vmID, req.Addresses)
	if err != nil {
		return nicError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}
