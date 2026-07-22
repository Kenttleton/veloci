package handler

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// ProjectionsHandler handles projection endpoints.
type ProjectionsHandler struct {
	s *store.Store
}

// NewProjectionsHandler creates a ProjectionsHandler.
func NewProjectionsHandler(s *store.Store) *ProjectionsHandler {
	return &ProjectionsHandler{s: s}
}

// projectionView is the API representation of a projection row.
type projectionView struct {
	ID                    string  `json:"id"`
	AccountID             *string `json:"account_id"`
	JobID                 string  `json:"job_id"`
	ProjectedDate         string  `json:"projected_date"`
	IncomeRatePerDay      float64 `json:"income_rate_per_day"`
	SpendRatePerDay float64 `json:"spend_rate_per_day"`
	MarginRatePerDay      float64 `json:"margin_rate_per_day"`
	ProjectedBalanceCents *int64  `json:"projected_balance_cents"`
	IsPinchPoint          bool    `json:"is_pinch_point"`
}

func toProjectionView(p store.Projection) projectionView {
	return projectionView{
		ID:                    p.ID,
		AccountID:             p.AccountID,
		JobID:                 p.JobID,
		ProjectedDate:         p.ProjectedDate.Format("2006-01-02"),
		IncomeRatePerDay:      p.IncomeRatePerDay,
		SpendRatePerDay: p.SpendRatePerDay,
		MarginRatePerDay:      p.MarginRatePerDay,
		ProjectedBalanceCents: p.ProjectedBalanceCents,
		IsPinchPoint:          p.IsPinchPoint,
	}
}

func (h *ProjectionsHandler) ListProjections(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	cursor := c.QueryParam("cursor")
	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}

	items, err := h.s.ListProjections(ctx, entityID, limit+1, cursor)
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
		c := store.EncodeCursor(last.ID, last.ProjectedDate)
		nextCursor = &c
	}

	views := make([]projectionView, len(items))
	for i, item := range items {
		views[i] = toProjectionView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

// RegisterProjectionsRoutes registers projection endpoints on the given Echo group.
func RegisterProjectionsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewProjectionsHandler(s)

	g.GET("/projections", h.ListProjections, middleware.RequirePermission(perms, "reports:read"))
}
