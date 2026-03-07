package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/maburvm/panel/internal/panel/service"
	"github.com/maburvm/panel/internal/shared/models"
)

// StorageHandler handles storage-related HTTP requests
type StorageHandler struct {
	storageService service.StorageService
}

// NewStorageHandler creates a new storage handler
func NewStorageHandler(storageService service.StorageService) *StorageHandler {
	return &StorageHandler{
		storageService: storageService,
	}
}

// RegisterRoutes registers all storage routes
func (h *StorageHandler) RegisterRoutes(e *echo.Echo, authMiddleware echo.MiddlewareFunc) {
	api := e.Group("/api/v1/storage", authMiddleware)

	// Pool routes
	api.GET("/pools", h.GetPools)
	api.GET("/pools/:id", h.GetPoolByID)
	api.GET("/pools/node/:nodeId", h.GetPoolsByNodeID)
	api.POST("/pools", h.CreatePool)
	api.PUT("/pools/:id", h.UpdatePool)
	api.DELETE("/pools/:id", h.DeletePool)

	// Volume routes
	api.GET("/pools/:poolId/volumes", h.GetVolumes)
	api.GET("/volumes/:id", h.GetVolumeByID)
	api.POST("/pools/:poolId/volumes", h.CreateVolume)
	api.DELETE("/volumes/:id", h.DeleteVolume)
}

// Pool handlers

// GetPools returns all storage pools
func (h *StorageHandler) GetPools(c echo.Context) error {
	pools, err := h.storageService.GetPools()
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve storage pools",
		})
	}

	return c.JSON(http.StatusOK, pools)
}

// GetPoolByID returns a specific storage pool
func (h *StorageHandler) GetPoolByID(c echo.Context) error {
	id := c.Param("id")

	pool, err := h.storageService.GetPoolByID(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve storage pool",
		})
	}

	if pool == nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "storage pool not found",
		})
	}

	return c.JSON(http.StatusOK, pool)
}

// GetPoolsByNodeID returns storage pools for a specific node
func (h *StorageHandler) GetPoolsByNodeID(c echo.Context) error {
	nodeID := c.Param("nodeId")

	pools, err := h.storageService.GetPoolsByNodeID(nodeID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve storage pools",
		})
	}

	return c.JSON(http.StatusOK, pools)
}

// CreatePool creates a new storage pool
func (h *StorageHandler) CreatePool(c echo.Context) error {
	var pool models.StoragePool
	if err := c.Bind(&pool); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	if pool.ID == "" {
		pool.ID = uuid.New().String()
	}

	if err := h.storageService.CreatePool(&pool); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to create storage pool",
		})
	}

	return c.JSON(http.StatusCreated, pool)
}

// UpdatePool updates an existing storage pool
func (h *StorageHandler) UpdatePool(c echo.Context) error {
	id := c.Param("id")

	existingPool, err := h.storageService.GetPoolByID(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve storage pool",
		})
	}

	if existingPool == nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "storage pool not found",
		})
	}

	var pool models.StoragePool
	if err := c.Bind(&pool); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	pool.ID = id

	if err := h.storageService.UpdatePool(id, &pool); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to update storage pool",
		})
	}

	return c.JSON(http.StatusOK, pool)
}

// DeletePool deletes a storage pool
func (h *StorageHandler) DeletePool(c echo.Context) error {
	id := c.Param("id")

	if err := h.storageService.DeletePool(id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to delete storage pool",
		})
	}

	return c.NoContent(http.StatusNoContent)
}

// Volume handlers

// GetVolumes returns all volumes for a pool
func (h *StorageHandler) GetVolumes(c echo.Context) error {
	poolID := c.Param("poolId")

	volumes, err := h.storageService.GetVolumes(poolID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve volumes",
		})
	}

	return c.JSON(http.StatusOK, volumes)
}

// GetVolumeByID returns a specific volume
func (h *StorageHandler) GetVolumeByID(c echo.Context) error {
	id := c.Param("id")

	volume, err := h.storageService.GetVolumeByID(id)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to retrieve volume",
		})
	}

	if volume == nil {
		return c.JSON(http.StatusNotFound, map[string]string{
			"error": "volume not found",
		})
	}

	return c.JSON(http.StatusOK, volume)
}

// CreateVolume creates a new storage volume
func (h *StorageHandler) CreateVolume(c echo.Context) error {
	poolID := c.Param("poolId")

	var volume models.StorageVolume
	if err := c.Bind(&volume); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{
			"error": "invalid request body",
		})
	}

	volume.PoolID = poolID
	if volume.ID == "" {
		volume.ID = uuid.New().String()
	}

	if err := h.storageService.CreateVolume(&volume); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to create volume",
		})
	}

	return c.JSON(http.StatusCreated, volume)
}

// DeleteVolume deletes a storage volume
func (h *StorageHandler) DeleteVolume(c echo.Context) error {
	id := c.Param("id")

	if err := h.storageService.DeleteVolume(id); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{
			"error": "failed to delete volume",
		})
	}

	return c.NoContent(http.StatusNoContent)
}
