package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// SnapshotsHandler handles snapshot endpoints.
type SnapshotsHandler struct {
	s *store.Store
}

// NewSnapshotsHandler creates a SnapshotsHandler.
func NewSnapshotsHandler(s *store.Store) *SnapshotsHandler {
	return &SnapshotsHandler{s: s}
}

// snapshotView is the API representation of a snapshot row.
type snapshotView struct {
	ID                     string  `json:"id"`
	NodeID                 string  `json:"node_id"`
	NodeType               string  `json:"node_type"`
	SnapshotDate           string  `json:"snapshot_date"`
	ComputedAsOf           string  `json:"computed_as_of"`
	JobID                  string  `json:"job_id"`
	ActualRatePerDay       float64 `json:"actual_rate_per_day"`
	ProjectedRatePerDay    float64 `json:"projected_rate_per_day"`
	DriftPerDay            float64 `json:"drift_per_day"`
	SlopePerDay            float64 `json:"slope_per_day"`
	RSquared               float64 `json:"r_squared"`
	TransactionCount       int     `json:"transaction_count"`
	WindowDaysUsed         int     `json:"window_days_used"`
	RollingWindowTotalCents int64  `json:"rolling_window_total_cents"`
	BalanceCents           *int64  `json:"balance_cents"`
}

func toSnapshotView(s store.Snapshot) snapshotView {
	return snapshotView{
		ID:                      s.ID,
		NodeID:                  s.NodeID,
		NodeType:                s.NodeType,
		SnapshotDate:            s.SnapshotDate.Format("2006-01-02"),
		ComputedAsOf:            s.ComputedAsOf.Format("2006-01-02T15:04:05Z07:00"),
		JobID:                   s.JobID,
		ActualRatePerDay:        s.ActualRatePerDay,
		ProjectedRatePerDay:     s.ProjectedRatePerDay,
		DriftPerDay:             s.DriftPerDay,
		SlopePerDay:             s.SlopePerDay,
		RSquared:                s.RSquared,
		TransactionCount:        s.TransactionCount,
		WindowDaysUsed:          s.WindowDaysUsed,
		RollingWindowTotalCents: s.RollingWindowTotalCents,
		BalanceCents:            s.BalanceCents,
	}
}

// snapshotSummaryView is the API representation of the aggregate summary.
type snapshotSummaryView struct {
	IncomeRate      float64 `json:"income_rate"`
	CommitmentsRate float64 `json:"commitments_rate"`
	MarginRate      float64 `json:"margin_rate"`
	DriftRate       float64 `json:"drift_rate"`
}

// snapshotHistoryView represents a single history data point.
type snapshotHistoryView struct {
	Period           string   `json:"period"`
	ActualRatePerDay float64  `json:"actual_rate_per_day"`
	OpenRate         *float64 `json:"open_rate,omitempty"`
	HighRate         *float64 `json:"high_rate,omitempty"`
	LowRate          *float64 `json:"low_rate,omitempty"`
	CloseRate        *float64 `json:"close_rate,omitempty"`
}

type listSnapshotsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listSnapshotsOutput struct {
	Body response.Envelope[[]snapshotView]
}

type getSnapshotSummaryOutput struct {
	Body response.Envelope[snapshotSummaryView]
}

type getSnapshotHistoryInput struct {
	NodeID      string `path:"node_id"`
	Cursor      string `query:"cursor"`
	Limit       int    `query:"limit" default:"60" minimum:"1" maximum:"180"`
	Granularity string `query:"granularity" default:"day"`
}

type getSnapshotHistoryOutput struct {
	Body response.Envelope[[]snapshotHistoryView]
}

func (h *SnapshotsHandler) ListSnapshots(ctx context.Context, input *listSnapshotsInput) (*listSnapshotsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListSnapshots(ctx, entityID, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, last.SnapshotDate)
		nextCursor = &c
	}

	views := make([]snapshotView, len(items))
	for i, item := range items {
		views[i] = toSnapshotView(item)
	}
	out := &listSnapshotsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *SnapshotsHandler) GetSnapshotSummary(ctx context.Context, _ *struct{}) (*getSnapshotSummaryOutput, error) {
	entityID := middleware.EntityID(ctx)

	summary, err := h.s.GetSnapshotSummary(ctx, entityID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &getSnapshotSummaryOutput{}
	out.Body = response.Single(snapshotSummaryView{
		IncomeRate:      summary.IncomeRate,
		CommitmentsRate: summary.CommitmentsRate,
		MarginRate:      summary.IncomeRate - summary.CommitmentsRate,
		DriftRate:       summary.DriftRate,
	})
	return out, nil
}

func (h *SnapshotsHandler) GetSnapshotHistory(ctx context.Context, input *getSnapshotHistoryInput) (*getSnapshotHistoryOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 60
	}

	before := time.Now()
	if input.Cursor != "" {
		t, err := time.Parse("2006-01-02", input.Cursor)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid cursor date")
		}
		before = t
	}

	granularity := input.Granularity
	if granularity == "" {
		granularity = "day"
	}

	items, err := h.s.GetSnapshotHistory(ctx, entityID, input.NodeID, before, limit, granularity)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	views := make([]snapshotHistoryView, len(items))
	for i, item := range items {
		v := snapshotHistoryView{
			ActualRatePerDay: item.ActualRatePerDay,
			OpenRate:         item.OpenRate,
			HighRate:         item.HighRate,
			LowRate:          item.LowRate,
			CloseRate:        item.CloseRate,
		}
		if granularity == "day" {
			v.Period = item.Period.Format("2006-01-02")
		} else {
			v.Period = item.Period.Format("2006-01-02")
		}
		views[i] = v
	}

	out := &getSnapshotHistoryOutput{}
	out.Body = response.Single(views)
	return out, nil
}

// RegisterSnapshotsRoutes registers snapshot endpoints on the given Huma API.
func RegisterSnapshotsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewSnapshotsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-snapshots",
		Method:      http.MethodGet,
		Path:        "/snapshots",
		Summary:     "List snapshots",
		Tags:        []string{"snapshots"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "reports:read")},
	}, h.ListSnapshots)

	huma.Register(api, huma.Operation{
		OperationID: "get-snapshot-summary",
		Method:      http.MethodGet,
		Path:        "/snapshots/summary",
		Summary:     "Get aggregate snapshot summary",
		Tags:        []string{"snapshots"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "reports:read")},
	}, h.GetSnapshotSummary)

	huma.Register(api, huma.Operation{
		OperationID: "get-snapshot-history",
		Method:      http.MethodGet,
		Path:        "/snapshots/{node_id}/history",
		Summary:     "Get snapshot history for a node",
		Tags:        []string{"snapshots"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "reports:read")},
	}, h.GetSnapshotHistory)
}
