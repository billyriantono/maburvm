package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	"gorm.io/gorm"

	"github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
)

// VirtualizorImportHandler serves the import wizard.
type VirtualizorImportHandler struct {
	svc *service.VirtualizorImportService
}

func NewVirtualizorImportHandler(svc *service.VirtualizorImportService) *VirtualizorImportHandler {
	return &VirtualizorImportHandler{svc: svc}
}

// RegisterVirtualizorImportRoutes mounts /api/v1/import/sources. It creates
// accounts, plans and IP pools, so it has a permission of its own.
func RegisterVirtualizorImportRoutes(e *echo.Echo, h *VirtualizorImportHandler, db *gorm.DB) {
	g := e.Group("/api/v1/import/sources", middleware.RequireAuth(db), middleware.RequirePermission("import:manage"))
	g.GET("", h.List)
	g.POST("", h.Create)
	g.PUT("/:id", h.Update)
	g.DELETE("/:id", h.Delete)
	g.POST("/:id/test", h.Test)
	g.GET("/:id/preview", h.Poll)
	g.POST("/:id/preview", h.Preview)
	g.POST("/:id/apply", h.Apply)
	g.POST("/:id/invites", h.Invites)
}

func importError(c echo.Context, err error) error {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, service.ErrImportSourceNotFound):
		status = http.StatusNotFound
	case errors.Is(err, service.ErrInvalidImportSource):
		status = http.StatusBadRequest
	case errors.Is(err, service.ErrImportNoPreview), errors.Is(err, service.ErrImportNoMail), errors.Is(err, service.ErrImportNoPanelURL),
		errors.Is(err, service.ErrImportReading):
		status = http.StatusConflict
	case errors.Is(err, service.ErrSourceUnreachable), errors.Is(err, service.ErrSourceRefused):
		// Not 502: that is the panel itself not answering, and the web client
		// would replace this message with that one.
		status = http.StatusUnprocessableEntity
	}
	return c.JSON(status, map[string]interface{}{"error": http.StatusText(status), "message": err.Error()})
}

func importActor(c echo.Context) service.ImportActor {
	a := service.ImportActor{IP: c.RealIP(), UserAgent: c.Request().UserAgent()}
	if u, ok := middleware.GetUserContext(c); ok {
		a.UserID = u.ID.String()
	}
	if len(a.UserAgent) > 500 {
		a.UserAgent = a.UserAgent[:500]
	}
	return a
}

func (h *VirtualizorImportHandler) List(c echo.Context) error {
	out, err := h.svc.ListSources(c.Request().Context())
	if err != nil {
		return importError(c, err)
	}
	if out == nil {
		out = []service.VirtualizorSource{}
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": out})
}

func (h *VirtualizorImportHandler) Create(c echo.Context) error {
	var in service.VirtualizorSourceInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	src, err := h.svc.CreateSource(c.Request().Context(), in)
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": src})
}

func (h *VirtualizorImportHandler) Update(c echo.Context) error {
	var in service.VirtualizorSourceInput
	if err := c.Bind(&in); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	src, err := h.svc.UpdateSource(c.Request().Context(), c.Param("id"), in)
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": src})
}

func (h *VirtualizorImportHandler) Delete(c echo.Context) error {
	if err := h.svc.DeleteSource(c.Request().Context(), c.Param("id")); err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": nil})
}

func (h *VirtualizorImportHandler) Test(c echo.Context) error {
	res, err := h.svc.TestSource(c.Request().Context(), c.Param("id"))
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": res})
}

// Poll handles GET /:id/preview: the plan as it stands, while a read of the
// source runs or after.
func (h *VirtualizorImportHandler) Poll(c echo.Context) error {
	plan, err := h.svc.Preview(c.Request().Context(), c.Param("id"), false, nil)
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// Preview handles POST /:id/preview: {refresh, options}. refresh starts a
// new read of the source; options are kept as its mapping choices.
func (h *VirtualizorImportHandler) Preview(c echo.Context) error {
	var body struct {
		Refresh bool                              `json:"refresh"`
		Options *service.VirtualizorImportOptions `json:"options"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	plan, err := h.svc.Preview(c.Request().Context(), c.Param("id"), body.Refresh, body.Options)
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": plan})
}

// Apply handles POST /:id/apply: {items: [{kind, key}]}.
func (h *VirtualizorImportHandler) Apply(c echo.Context) error {
	var body struct {
		Items []service.ImportItemRef `json:"items"`
	}
	if err := c.Bind(&body); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Invalid request body"})
	}
	if len(body.Items) == 0 || len(body.Items) > 500 {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Bad Request", "message": "Send between 1 and 500 items at a time"})
	}
	results, err := h.svc.Apply(c.Request().Context(), c.Param("id"), body.Items, importActor(c))
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"results": results}})
}

// Invites handles POST /:id/invites: email the accounts the import created.
func (h *VirtualizorImportHandler) Invites(c echo.Context) error {
	n, err := h.svc.SendInvites(c.Request().Context(), c.Param("id"), importActor(c))
	if err != nil {
		return importError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": map[string]interface{}{"sent": n}})
}
