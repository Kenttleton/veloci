package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

// EntriesHandler handles entry (budget line) endpoints.
type EntriesHandler struct {
	s   *store.Store
	pub *queue.Publisher
}

// NewEntriesHandler creates an EntriesHandler.
func NewEntriesHandler(s *store.Store, pub *queue.Publisher) *EntriesHandler {
	return &EntriesHandler{s: s, pub: pub}
}

// entryView is the API representation of an entry with computed budget fields.
type entryView struct {
	ID           string          `json:"id"`
	LabelID      *string         `json:"label_id"`
	LabelName    *string         `json:"label_name"`
	Name         string          `json:"name"`
	Direction    string          `json:"direction"`
	EntryType    string          `json:"entry_type"`
	Period       string          `json:"period"`
	Status       string          `json:"status"`
	Source       string          `json:"source"`
	Priority     int             `json:"priority"`
	ActualRate   float64         `json:"actual_rate"`
	ProjectedRate *float64       `json:"projected_rate"`
	DriftRate    float64         `json:"drift_rate"`
	Tag          *string         `json:"tag"`
	Conditions   json.RawMessage `json:"conditions"`
	StartDate    string          `json:"start_date"`
	EndDate      *string         `json:"end_date"`
	CreatedAt    string          `json:"created_at"`
}

func toEntryView(e store.EntryRow) entryView {
	name := entryName(e)
	period := fmt.Sprintf("%dd", e.PeriodDays)

	var actualRate float64
	if e.ActualRatePerDay != nil {
		actualRate = *e.ActualRatePerDay
	}

	var driftRate float64
	if e.SnapshotDriftPerDay != nil {
		driftRate = *e.SnapshotDriftPerDay
	}

	var tag *string
	if e.SnapshotDriftPerDay != nil {
		if *e.SnapshotDriftPerDay > 0 {
			s := "boost"
			tag = &s
		} else if *e.SnapshotDriftPerDay < 0 {
			s := "hit"
			tag = &s
		}
	}

	var endDate *string
	if e.EndDate != nil {
		s := e.EndDate.Format("2006-01-02")
		endDate = &s
	}

	return entryView{
		ID:           e.ID,
		LabelID:      e.LabelID,
		LabelName:    e.LabelName,
		Name:         name,
		Direction:    e.Direction,
		EntryType:    e.EntryType,
		Period:       period,
		Status:       e.Status,
		Source:       e.Source,
		Priority:     e.Priority,
		ActualRate:   actualRate,
		ProjectedRate: e.ProjectedRatePerDay,
		DriftRate:    driftRate,
		Tag:          tag,
		Conditions:   e.Conditions,
		StartDate:    e.StartDate.Format("2006-01-02"),
		EndDate:      endDate,
		CreatedAt:    e.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func entryName(e store.EntryRow) string {
	if e.LabelName != nil && *e.LabelName != "" {
		return *e.LabelName
	}
	if len(e.ID) >= 8 {
		return e.ID[:8]
	}
	return e.ID
}

type listEntriesInput struct {
	DateFrom  string `query:"date_from"`
	DateTo    string `query:"date_to"`
	AccountID string `query:"account_id"`
	Status    string `query:"status"`
	Cursor    string `query:"cursor"`
	Limit     int    `query:"limit" default:"200" minimum:"1" maximum:"500"`
}

type listEntriesOutput struct {
	Body response.Envelope[[]entryView]
}

type getEntryInput struct {
	PathID string `path:"id"`
}

type getEntryOutput struct {
	Body response.Envelope[entryView]
}

type createEntryInput struct {
	Body struct {
		LabelID             *string         `json:"label_id"`
		Direction           string          `json:"direction"            required:"true"`
		EntryType           string          `json:"entry_type"           required:"true"`
		PeriodDays          int             `json:"period_days"          required:"true"`
		VariableMethod      *string         `json:"variable_method"`
		ProjectedRatePerDay *float64        `json:"projected_rate_per_day"`
		Conditions          json.RawMessage `json:"conditions"`
		Priority            int             `json:"priority"`
		Source              string          `json:"source"`
		ProjectTentatively  bool            `json:"project_tentatively"`
		StartDate           string          `json:"start_date" required:"true"`
		EndDate             *string         `json:"end_date"`
	}
}

type createEntryOutput struct {
	Body response.Envelope[entryView]
}

type updateEntryInput struct {
	PathID string `path:"id"`
	Body   struct {
		LabelID             *string         `json:"label_id"`
		Direction           string          `json:"direction"            required:"true"`
		EntryType           string          `json:"entry_type"           required:"true"`
		PeriodDays          int             `json:"period_days"          required:"true"`
		VariableMethod      *string         `json:"variable_method"`
		ProjectedRatePerDay *float64        `json:"projected_rate_per_day"`
		Conditions          json.RawMessage `json:"conditions"`
		Priority            int             `json:"priority"`
		Status              string          `json:"status" required:"true"`
		ProjectTentatively  bool            `json:"project_tentatively"`
		StartDate           string          `json:"start_date" required:"true"`
		EndDate             *string         `json:"end_date"`
	}
}

type updateEntryOutput struct {
	Body response.Envelope[entryView]
}

type deleteEntryInput struct {
	PathID string `path:"id"`
}

type previewEntryInput struct {
	Body struct {
		Conditions json.RawMessage `json:"conditions" required:"true"`
	}
}

type previewEntryOutput struct {
	Body struct {
		MatchedCount   int      `json:"matched_count"`
		TransactionIDs []string `json:"transaction_ids"`
	}
}

func (h *EntriesHandler) ListEntries(ctx context.Context, input *listEntriesInput) (*listEntriesOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListEntries(ctx, entityID, input.DateFrom, input.DateTo, input.AccountID, input.Status, limit+1, input.Cursor)
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
		c := store.EncodeDateCursor(last.ID, last.StartDate)
		nextCursor = &c
	}

	views := make([]entryView, len(items))
	for i, item := range items {
		views[i] = toEntryView(item)
	}
	out := &listEntriesOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *EntriesHandler) GetEntry(ctx context.Context, input *getEntryInput) (*getEntryOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetEntry(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getEntryOutput{}
	out.Body = response.Single(toEntryView(item))
	return out, nil
}

func (h *EntriesHandler) CreateEntry(ctx context.Context, input *createEntryInput) (*createEntryOutput, error) {
	entityID := middleware.EntityID(ctx)

	startDate, err := time.Parse("2006-01-02", input.Body.StartDate)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("invalid start_date")
	}
	var endDate *time.Time
	if input.Body.EndDate != nil {
		t, err := time.Parse("2006-01-02", *input.Body.EndDate)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid end_date")
		}
		endDate = &t
	}

	source := input.Body.Source
	if source == "" {
		source = "user"
	}

	item, err := h.s.CreateEntry(ctx, entityID, store.CreateEntryInput{
		LabelID:             input.Body.LabelID,
		Direction:           input.Body.Direction,
		EntryType:           input.Body.EntryType,
		PeriodDays:          input.Body.PeriodDays,
		VariableMethod:      input.Body.VariableMethod,
		ProjectedRatePerDay: input.Body.ProjectedRatePerDay,
		Conditions:          input.Body.Conditions,
		Priority:            input.Body.Priority,
		Source:              source,
		ProjectTentatively:  input.Body.ProjectTentatively,
		StartDate:           startDate,
		EndDate:             endDate,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createEntryOutput{}
	out.Body = response.Single(toEntryView(item))
	return out, nil
}

func (h *EntriesHandler) UpdateEntry(ctx context.Context, input *updateEntryInput) (*updateEntryOutput, error) {
	entityID := middleware.EntityID(ctx)

	startDate, err := time.Parse("2006-01-02", input.Body.StartDate)
	if err != nil {
		return nil, huma.Error422UnprocessableEntity("invalid start_date")
	}
	var endDate *time.Time
	if input.Body.EndDate != nil {
		t, err := time.Parse("2006-01-02", *input.Body.EndDate)
		if err != nil {
			return nil, huma.Error422UnprocessableEntity("invalid end_date")
		}
		endDate = &t
	}

	item, err := h.s.UpdateEntry(ctx, entityID, input.PathID, store.UpdateEntryInput{
		LabelID:             input.Body.LabelID,
		Direction:           input.Body.Direction,
		EntryType:           input.Body.EntryType,
		PeriodDays:          input.Body.PeriodDays,
		VariableMethod:      input.Body.VariableMethod,
		ProjectedRatePerDay: input.Body.ProjectedRatePerDay,
		Conditions:          input.Body.Conditions,
		Priority:            input.Body.Priority,
		Status:              input.Body.Status,
		ProjectTentatively:  input.Body.ProjectTentatively,
		StartDate:           startDate,
		EndDate:             endDate,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateEntryOutput{}
	out.Body = response.Single(toEntryView(item))
	return out, nil
}

func (h *EntriesHandler) DeleteEntry(ctx context.Context, input *deleteEntryInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	err := h.s.DeleteEntry(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

type approveEntryInput struct {
	PathID string `path:"id"`
}

type rejectEntryInput struct {
	PathID string `path:"id"`
}

func (h *EntriesHandler) ApproveEntry(ctx context.Context, input *approveEntryInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	alertType, err := h.s.ApproveEntryReview(ctx, entityID, input.PathID, userID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	if alertType != "drift" {
		meta, _ := json.Marshal(map[string]string{})
		if job, err := h.s.CreateJob(ctx, entityID, "account.analyze", userID, meta); err == nil {
			h.pub.Publish(ctx, queue.Job{ //nolint:errcheck
				JobID:    job.ID,
				Type:     "account.analyze",
				EntityID: entityID,
				Metadata: meta,
			})
		}
	}
	return nil, nil
}

func (h *EntriesHandler) RejectEntry(ctx context.Context, input *rejectEntryInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	userID := middleware.UserID(ctx)

	if err := h.s.RejectEntryReview(ctx, entityID, input.PathID, userID); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *EntriesHandler) PreviewEntry(ctx context.Context, input *previewEntryInput) (*previewEntryOutput, error) {
	entityID := middleware.EntityID(ctx)

	count, ids, err := h.s.PreviewConditions(ctx, entityID, input.Body.Conditions)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	if ids == nil {
		ids = []string{}
	}

	out := &previewEntryOutput{}
	out.Body.MatchedCount = count
	out.Body.TransactionIDs = ids
	return out, nil
}

// RegisterEntriesRoutes registers entry endpoints on the given Huma API.
func RegisterEntriesRoutes(api huma.API, s *store.Store, pub *queue.Publisher, perms middleware.PermissionCache) {
	h := NewEntriesHandler(s, pub)

	huma.Register(api, huma.Operation{
		OperationID: "list-entries",
		Method:      http.MethodGet,
		Path:        "/entries",
		Summary:     "List budget entries",
		Tags:        []string{"entries"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListEntries)

	huma.Register(api, huma.Operation{
		OperationID: "create-entry",
		Method:      http.MethodPost,
		Path:        "/entries",
		Summary:     "Create a budget entry",
		Tags:        []string{"entries"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.CreateEntry)

	huma.Register(api, huma.Operation{
		OperationID: "get-entry",
		Method:      http.MethodGet,
		Path:        "/entries/{id}",
		Summary:     "Get a budget entry",
		Tags:        []string{"entries"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetEntry)

	huma.Register(api, huma.Operation{
		OperationID: "update-entry",
		Method:      http.MethodPut,
		Path:        "/entries/{id}",
		Summary:     "Update a budget entry",
		Tags:        []string{"entries"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.UpdateEntry)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-entry",
		Method:        http.MethodDelete,
		Path:          "/entries/{id}",
		Summary:       "Delete a budget entry",
		Tags:          []string{"entries"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.DeleteEntry)

	huma.Register(api, huma.Operation{
		OperationID:   "approve-entry",
		Method:        http.MethodPost,
		Path:          "/entries/{id}/approve",
		Summary:       "Approve a pending entry",
		Tags:          []string{"entries"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.ApproveEntry)

	huma.Register(api, huma.Operation{
		OperationID:   "reject-entry",
		Method:        http.MethodPost,
		Path:          "/entries/{id}/reject",
		Summary:       "Reject a pending entry",
		Tags:          []string{"entries"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "entries:write")},
	}, h.RejectEntry)

	huma.Register(api, huma.Operation{
		OperationID: "preview-entry",
		Method:      http.MethodPost,
		Path:        "/entries/preview",
		Summary:     "Preview condition matching",
		Tags:        []string{"entries"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.PreviewEntry)
}
