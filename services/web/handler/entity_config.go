package handler

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// EntityConfigHandler handles entity configuration endpoints.
type EntityConfigHandler struct {
	s *store.Store
}

// NewEntityConfigHandler creates an EntityConfigHandler.
func NewEntityConfigHandler(s *store.Store) *EntityConfigHandler {
	return &EntityConfigHandler{s: s}
}

type entityConfigView struct {
	SystemWindowDays int `json:"system_window_days"`
}

func toEntityConfigView(c store.EntityConfig) entityConfigView {
	return entityConfigView{SystemWindowDays: c.SystemWindowDays}
}

func (h *EntityConfigHandler) GetEntityConfig(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	cfg, err := h.s.GetEntityConfig(ctx, entityID)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toEntityConfigView(cfg)))
}

type updateEntityConfigBody struct {
	SystemWindowDays int `json:"system_window_days"`
}

func (h *EntityConfigHandler) UpdateEntityConfig(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body updateEntityConfigBody
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid request body")
	}
	if body.SystemWindowDays < 1 || body.SystemWindowDays > 365 {
		return echo.NewHTTPError(http.StatusBadRequest, "system_window_days must be between 1 and 365")
	}

	cfg, err := h.s.UpdateEntityConfig(ctx, entityID, body.SystemWindowDays)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toEntityConfigView(cfg)))
}

func RegisterEntityConfigRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewEntityConfigHandler(s)
	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "labels:write"))

	read.GET("/entity/config", h.GetEntityConfig)
	write.PUT("/entity/config", h.UpdateEntityConfig)
}
