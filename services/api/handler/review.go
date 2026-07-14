package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

// ReviewHandler handles review queue endpoints.
type ReviewHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewReviewHandler creates a ReviewHandler.
func NewReviewHandler(s *store.Store, pub *queue.Publisher) *ReviewHandler {
	return &ReviewHandler{s: s, pub: pub}
}

// reviewView is the API representation of a review_queue item.
type reviewView struct {
	ID                      string          `json:"id"`
	EntryID                 string          `json:"entry_id"`
	JobID                   string          `json:"job_id"`
	SuggestedName           *string         `json:"suggested_name"`
	SuggestedEntryType      *string         `json:"suggested_entry_type"`
	SuggestedConditions     json.RawMessage `json:"suggested_conditions"`
	SuggestedRatePerDay     *float64        `json:"suggested_rate_per_day"`
	MatchedTransactionCount int             `json:"matched_transaction_count"`
	AlertType               string          `json:"alert_type"`
	Confidence              *float64        `json:"confidence"`
	MerchantConfidence      *float64        `json:"merchant_confidence"`
	TimingConfidence        *float64        `json:"timing_confidence"`
	AmountConfidence        *float64        `json:"amount_confidence"`
	SampleMerchants         []string        `json:"sample_merchants"`
	Status                  string          `json:"status"`
	ReviewedBy              *string         `json:"reviewed_by"`
	ReviewedAt              *string         `json:"reviewed_at"`
}

func toReviewView(r store.ReviewItem) reviewView {
	v := reviewView{
		ID:                      r.ID,
		EntryID:                 r.EntryID,
		JobID:                   r.JobID,
		SuggestedName:           r.SuggestedName,
		SuggestedEntryType:      r.SuggestedEntryType,
		SuggestedConditions:     r.SuggestedConditions,
		SuggestedRatePerDay:     r.SuggestedRatePerDay,
		MatchedTransactionCount: r.MatchedTransactionCount,
		AlertType:               r.AlertType,
		Confidence:              r.Confidence,
		MerchantConfidence:      r.MerchantConfidence,
		TimingConfidence:        r.TimingConfidence,
		AmountConfidence:        r.AmountConfidence,
		SampleMerchants:         r.SampleMerchants,
		Status:                  r.Status,
		ReviewedBy:              r.ReviewedBy,
	}
	if r.ReviewedAt != nil {
		s := r.ReviewedAt.Format("2006-01-02T15:04:05Z07:00")
		v.ReviewedAt = &s
	}
	if v.SampleMerchants == nil {
		v.SampleMerchants = []string{}
	}
	return v
}

type listReviewInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listReviewOutput struct {
	Body response.Envelope[[]reviewView]
}

type getReviewInput struct {
	PathID string `path:"id"`
}

type getReviewOutput struct {
	Body response.Envelope[reviewView]
}

type updateReviewInput struct {
	PathID string `path:"id"`
	Body   struct {
		Status string `json:"status" required:"true"`
	}
}

type updateReviewOutput struct {
	Body response.Envelope[reviewView]
}

type approveReviewInput struct {
	PathID string `path:"id"`
}

type rejectReviewInput struct {
	PathID string `path:"id"`
}

func (h *ReviewHandler) ListReview(ctx context.Context, input *listReviewInput) (*listReviewOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListReviewItems(ctx, entityID, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, time.Now())
		nextCursor = &c
	}

	views := make([]reviewView, len(items))
	for i, item := range items {
		views[i] = toReviewView(item)
	}
	out := &listReviewOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *ReviewHandler) UpdateReview(ctx context.Context, input *updateReviewInput) (*updateReviewOutput, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	if err := h.s.UpdateReviewStatus(ctx, entityID, input.PathID, input.Body.Status, userID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	item, err := h.s.GetReviewItem(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateReviewOutput{}
	out.Body = response.Single(toReviewView(item))
	return out, nil
}

func (h *ReviewHandler) ApproveReview(ctx context.Context, input *approveReviewInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	item, err := h.s.GetReviewItem(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	if item.Status != "pending" {
		return nil, huma.Error409Conflict("review item is not pending")
	}

	switch item.AlertType {
	case "new":
		if err := h.s.ActivateEntry(ctx, entityID, item.EntryID); err != nil {
			return nil, huma.Error500InternalServerError("internal error")
		}
	case "ended":
		if err := h.s.EndEntry(ctx, entityID, item.EntryID, time.Now()); err != nil {
			return nil, huma.Error500InternalServerError("internal error")
		}
	}

	if err := h.s.UpdateReviewStatus(ctx, entityID, input.PathID, "approved", userID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	meta, _ := json.Marshal(map[string]string{})
	job, err := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta)
	if err == nil {
		h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
			JobID:    job.ID,
			Type:     "account.analyze",
			EntityID: entityID,
			Metadata: meta,
		})
	}

	return nil, nil
}

func (h *ReviewHandler) RejectReview(ctx context.Context, input *rejectReviewInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	item, err := h.s.GetReviewItem(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	if err := h.s.UpdateReviewStatus(ctx, entityID, input.PathID, "rejected", userID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	if item.AlertType == "new" {
		if err := h.s.DeactivateEntry(ctx, entityID, item.EntryID); err != nil {
			return nil, huma.Error500InternalServerError("internal error")
		}
	}

	return nil, nil
}

// RegisterReviewRoutes registers review queue endpoints on the given Huma API.
func RegisterReviewRoutes(api huma.API, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewReviewHandler(s, pub)

	huma.Register(api, huma.Operation{
		OperationID: "list-review",
		Method:      http.MethodGet,
		Path:        "/review",
		Summary:     "List review queue items",
		Tags:        []string{"review"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListReview)

	huma.Register(api, huma.Operation{
		OperationID: "update-review",
		Method:      http.MethodPut,
		Path:        "/review/{id}",
		Summary:     "Update a review queue item status",
		Tags:        []string{"review"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "review:write")},
	}, h.UpdateReview)

	huma.Register(api, huma.Operation{
		OperationID:   "approve-review",
		Method:        http.MethodPost,
		Path:          "/review/{id}/approve",
		Summary:       "Approve a review queue item",
		Tags:          []string{"review"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "review:write")},
	}, h.ApproveReview)

	huma.Register(api, huma.Operation{
		OperationID:   "reject-review",
		Method:        http.MethodPost,
		Path:          "/review/{id}/reject",
		Summary:       "Reject a review queue item",
		Tags:          []string{"review"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "review:write")},
	}, h.RejectReview)
}
