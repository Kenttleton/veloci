package handler

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// AdminHandler handles server admin endpoints.
type AdminHandler struct {
	s *store.Store
}

// NewAdminHandler creates an AdminHandler.
func NewAdminHandler(s *store.Store) *AdminHandler {
	return &AdminHandler{s: s}
}

// adminStatusView is the API representation of the service status.
type adminStatusView struct {
	Status    string `json:"status"`
	Timestamp string `json:"timestamp"`
}

// adminEntityView is a minimal entity summary for admin listing.
type adminEntityView struct {
	EntityID  string `json:"entity_id"`
	UserCount int    `json:"user_count"`
}

func requireServerAdmin(c echo.Context) error {
	if middleware.SystemRole(c.Request().Context()) != "server_admin" {
		return echo.NewHTTPError(http.StatusForbidden, "forbidden")
	}
	return nil
}

func (h *AdminHandler) GetStatus(c echo.Context) error {
	if err := requireServerAdmin(c); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, response.Single(adminStatusView{
		Status:    "ok",
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	}))
}

func (h *AdminHandler) ListEntities(c echo.Context) error {
	if err := requireServerAdmin(c); err != nil {
		return err
	}

	entities, err := h.s.ListEntities(c.Request().Context())
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	views := make([]adminEntityView, len(entities))
	for i, e := range entities {
		views[i] = adminEntityView{EntityID: e.EntityID, UserCount: e.UserCount}
	}

	return c.JSON(http.StatusOK, response.Single(views))
}

// RegisterAdminRoutes registers admin endpoints on the given Echo group.
func RegisterAdminRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewAdminHandler(s)

	g.GET("/admin/status", h.GetStatus)
	g.GET("/admin/entities", h.ListEntities)
}
