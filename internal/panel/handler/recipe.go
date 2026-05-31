package handler

import (
	"errors"
	"net/http"

	"github.com/labstack/echo/v4"
	panelMiddleware "github.com/maburvm/panel/internal/panel/middleware"
	"github.com/maburvm/panel/internal/panel/service"
	"gorm.io/gorm"
)

// RecipeHandler handles per-user first-boot recipe endpoints.
type RecipeHandler struct {
	service *service.RecipeService
}

// NewRecipeHandler creates a new RecipeHandler.
func NewRecipeHandler(s *service.RecipeService) *RecipeHandler {
	return &RecipeHandler{service: s}
}

// ListRecipes handles GET /api/v1/recipes (current user's recipes).
func (h *RecipeHandler) ListRecipes(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	recipes, err := h.service.ListRecipes(c.Request().Context(), user.ID.String())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": "Failed to list recipes"})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": recipes})
}

// CreateRecipe handles POST /api/v1/recipes.
func (h *RecipeHandler) CreateRecipe(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	var req service.RecipeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	recipe, err := h.service.CreateRecipe(c.Request().Context(), user.ID.String(), req)
	if err != nil {
		return recipeError(c, err)
	}
	return c.JSON(http.StatusCreated, map[string]interface{}{"success": true, "data": recipe})
}

// UpdateRecipe handles PUT /api/v1/recipes/:id.
func (h *RecipeHandler) UpdateRecipe(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	var req service.RecipeRequest
	if err := c.Bind(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": "Invalid request body"})
	}
	recipe, err := h.service.UpdateRecipe(c.Request().Context(), c.Param("id"), user.ID.String(), req)
	if err != nil {
		return recipeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "data": recipe})
}

// DeleteRecipe handles DELETE /api/v1/recipes/:id (current user's recipe only).
func (h *RecipeHandler) DeleteRecipe(c echo.Context) error {
	user, ok := panelMiddleware.GetUserContext(c)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]interface{}{"error": "Unauthorized"})
	}
	if err := h.service.DeleteRecipe(c.Request().Context(), c.Param("id"), user.ID.String()); err != nil {
		return recipeError(c, err)
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"success": true, "message": "Recipe deleted"})
}

// recipeError maps service errors to HTTP responses.
func recipeError(c echo.Context, err error) error {
	switch {
	case errors.Is(err, service.ErrRecipeNotFound):
		return c.JSON(http.StatusNotFound, map[string]interface{}{"error": "Recipe not found"})
	case errors.Is(err, service.ErrRecipeDuplicate):
		return c.JSON(http.StatusConflict, map[string]interface{}{"error": err.Error()})
	case errors.Is(err, service.ErrRecipeInvalid):
		return c.JSON(http.StatusBadRequest, map[string]interface{}{"error": err.Error()})
	default:
		return c.JSON(http.StatusInternalServerError, map[string]interface{}{"error": err.Error()})
	}
}

// RegisterRecipeRoutes registers per-user recipe routes (all require auth).
func RegisterRecipeRoutes(e *echo.Echo, h *RecipeHandler, db *gorm.DB) {
	g := e.Group("/api/v1/recipes")
	g.Use(panelMiddleware.RequireAuth(db))
	g.GET("", h.ListRecipes)
	g.POST("", h.CreateRecipe)
	g.PUT("/:id", h.UpdateRecipe)
	g.DELETE("/:id", h.DeleteRecipe)
}
