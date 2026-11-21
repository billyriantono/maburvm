package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
)

// GetPreflight handles GET /api/v1/nodes/:id/preflight: the stored report.
func (h *NodeHandler) GetPreflight(c echo.Context) error {
	rep, err := h.service.StoredPreflight(c.Request().Context(), c.Param("id"))
	return preflightResponse(c, rep, err)
}

// RunPreflight handles POST /api/v1/nodes/:id/preflight: asks the node now.
func (h *NodeHandler) RunPreflight(c echo.Context) error {
	rep, err := h.service.RunPreflight(c.Request().Context(), c.Param("id"))
	return preflightResponse(c, rep, err)
}

// GetAgentSettings handles GET /api/v1/nodes/:id/agent-settings: the agent's
// own environment, asked now, secrets only as "set".
func (h *NodeHandler) GetAgentSettings(c echo.Context) error {
	ctx, cancel := context.WithTimeout(c.Request().Context(), 10*time.Second)
	defer cancel()
	rep, err := h.service.AgentSettings(ctx, c.Param("id"))
	return preflightResponse(c, rep, err)
}

func preflightResponse(c echo.Context, rep any, err error) error {
	switch {
	case errors.Is(err, service.ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"success": false, "error": "Not Found", "message": "Node not found"})
	case errors.Is(err, service.ErrNodeUnreachable):
		return c.JSON(http.StatusBadGateway, map[string]any{"success": false, "error": "Bad Gateway", "message": err.Error()})
	case err != nil:
		return c.JSON(http.StatusInternalServerError, map[string]any{"success": false, "error": "Internal Server Error", "message": err.Error()})
	}
	return c.JSON(http.StatusOK, map[string]any{"success": true, "data": rep})
}
