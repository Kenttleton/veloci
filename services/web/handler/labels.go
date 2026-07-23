package handler

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// LabelsHandler handles label endpoints.
type LabelsHandler struct {
	s *store.Store
}

// NewLabelsHandler creates a LabelsHandler.
func NewLabelsHandler(s *store.Store) *LabelsHandler {
	return &LabelsHandler{s: s}
}

// labelView is the API representation of a label.
type labelView struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Scope     *string `json:"scope"`
	CreatedAt string  `json:"created_at"`
}

func toLabelView(l store.Label) labelView {
	return labelView{
		ID:        l.ID,
		Name:      l.Name,
		Scope:     l.Scope,
		CreatedAt: l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *LabelsHandler) ListLabels(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	cursor := c.QueryParam("cursor")
	limit := 50
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		limit = l
	}

	items, err := h.s.ListLabels(ctx, entityID, limit+1, cursor)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &cur
	}

	views := make([]labelView, len(items))
	for i, item := range items {
		views[i] = toLabelView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *LabelsHandler) GetLabel(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	item, err := h.s.GetLabel(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toLabelView(item)))
}

func (h *LabelsHandler) CreateLabel(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	item, err := h.s.CreateLabel(ctx, entityID, body.Name)
	if err != nil {
		// Unique constraint violation
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return echo.NewHTTPError(http.StatusConflict, "label name already exists")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toLabelView(item)))
}

func (h *LabelsHandler) UpdateLabel(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	item, err := h.s.UpdateLabel(ctx, entityID, id, body.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if errors.Is(err, store.ErrSystemLabel) {
		return echo.NewHTTPError(http.StatusForbidden, "system label cannot be modified")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return echo.NewHTTPError(http.StatusConflict, "label name already exists")
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toLabelView(item)))
}

func (h *LabelsHandler) DeleteLabel(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	err := h.s.DeleteLabel(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if errors.Is(err, store.ErrSystemLabel) {
		return echo.NewHTTPError(http.StatusForbidden, "system label cannot be modified")
	}
	if errors.Is(err, store.ErrLabelInUse) {
		return echo.NewHTTPError(http.StatusConflict, "label is in use by entries")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *LabelsHandler) ListLabelEntries(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	cursor := c.QueryParam("cursor")
	limit := 50
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		limit = l
	}

	items, err := h.s.ListEntriesByLabel(ctx, entityID, id, limit+1, cursor)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &cur
	}

	views := make([]entryView, len(items))
	for i, item := range items {
		views[i] = toEntryView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

// RegisterLabelsRoutes registers label endpoints on the given Echo group.
func RegisterLabelsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewLabelsHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "labels:write"))

	read.GET("/labels", h.ListLabels)
	write.POST("/labels", h.CreateLabel)
	read.GET("/labels/:id", h.GetLabel)
	write.PUT("/labels/:id", h.UpdateLabel)
	write.DELETE("/labels/:id", h.DeleteLabel)
	read.GET("/labels/:id/entries", h.ListLabelEntries)
}
