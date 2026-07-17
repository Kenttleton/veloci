package handler

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
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
	CommitmentRatePerDay  float64 `json:"commitment_rate_per_day"`
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
		CommitmentRatePerDay:  p.CommitmentRatePerDay,
		MarginRatePerDay:      p.MarginRatePerDay,
		ProjectedBalanceCents: p.ProjectedBalanceCents,
		IsPinchPoint:          p.IsPinchPoint,
	}
}

type listProjectionsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listProjectionsOutput struct {
	Body response.Envelope[[]projectionView]
}

func (h *ProjectionsHandler) ListProjections(ctx context.Context, input *listProjectionsInput) (*listProjectionsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListProjections(ctx, entityID, limit+1, input.Cursor)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
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
	out := &listProjectionsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

// RegisterProjectionsRoutes registers projection endpoints on the given Huma API.
func RegisterProjectionsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewProjectionsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-projections",
		Method:      http.MethodGet,
		Path:        "/projections",
		Summary:     "List balance projections",
		Tags:        []string{"projections"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "reports:read")},
	}, h.ListProjections)
}
