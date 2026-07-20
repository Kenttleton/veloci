package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

func toLabelView(l store.Label) labelView {
	return labelView{
		ID:        l.ID,
		Name:      l.Name,
		CreatedAt: l.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type listLabelsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listLabelsOutput struct {
	Body response.Envelope[[]labelView]
}

type getLabelInput struct {
	PathID string `path:"id"`
}

type getLabelOutput struct {
	Body response.Envelope[labelView]
}

type createLabelInput struct {
	Body struct {
		Name string `json:"name" required:"true"`
	}
}

type createLabelOutput struct {
	Body response.Envelope[labelView]
}

type updateLabelInput struct {
	PathID string `path:"id"`
	Body   struct {
		Name string `json:"name" required:"true"`
	}
}

type updateLabelOutput struct {
	Body response.Envelope[labelView]
}

type deleteLabelInput struct {
	PathID string `path:"id"`
}

type listLabelEntriesInput struct {
	PathID string `path:"id"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listLabelEntriesOutput struct {
	Body response.Envelope[[]entryView]
}

func (h *LabelsHandler) ListLabels(ctx context.Context, input *listLabelsInput) (*listLabelsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListLabels(ctx, entityID, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &c
	}

	views := make([]labelView, len(items))
	for i, item := range items {
		views[i] = toLabelView(item)
	}
	out := &listLabelsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *LabelsHandler) GetLabel(ctx context.Context, input *getLabelInput) (*getLabelOutput, error) {
	entityID := middleware.EntityID(ctx)
	item, err := h.s.GetLabel(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getLabelOutput{}
	out.Body = response.Single(toLabelView(item))
	return out, nil
}

func (h *LabelsHandler) CreateLabel(ctx context.Context, input *createLabelInput) (*createLabelOutput, error) {
	entityID := middleware.EntityID(ctx)
	item, err := h.s.CreateLabel(ctx, entityID, input.Body.Name)
	if err != nil {
		// Unique constraint violation
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("label name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createLabelOutput{}
	out.Body = response.Single(toLabelView(item))
	return out, nil
}

func (h *LabelsHandler) UpdateLabel(ctx context.Context, input *updateLabelInput) (*updateLabelOutput, error) {
	entityID := middleware.EntityID(ctx)
	item, err := h.s.UpdateLabel(ctx, entityID, input.PathID, input.Body.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		if strings.Contains(err.Error(), "unique") || strings.Contains(err.Error(), "duplicate") {
			return nil, huma.Error409Conflict("label name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateLabelOutput{}
	out.Body = response.Single(toLabelView(item))
	return out, nil
}

func (h *LabelsHandler) DeleteLabel(ctx context.Context, input *deleteLabelInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)
	err := h.s.DeleteLabel(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *LabelsHandler) ListLabelEntries(ctx context.Context, input *listLabelEntriesInput) (*listLabelEntriesOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListEntriesByLabel(ctx, entityID, input.PathID, limit+1, input.Cursor)
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
		c := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &c
	}

	views := make([]entryView, len(items))
	for i, item := range items {
		views[i] = toEntryView(item)
	}
	out := &listLabelEntriesOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

// RegisterLabelsRoutes registers label endpoints on the given Huma API.
func RegisterLabelsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewLabelsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-labels",
		Method:      http.MethodGet,
		Path:        "/labels",
		Summary:     "List all labels",
		Tags:        []string{"labels"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListLabels)

	huma.Register(api, huma.Operation{
		OperationID: "create-label",
		Method:      http.MethodPost,
		Path:        "/labels",
		Summary:     "Create a label",
		Tags:        []string{"labels"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.CreateLabel)

	huma.Register(api, huma.Operation{
		OperationID: "get-label",
		Method:      http.MethodGet,
		Path:        "/labels/{id}",
		Summary:     "Get a label",
		Tags:        []string{"labels"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetLabel)

	huma.Register(api, huma.Operation{
		OperationID: "update-label",
		Method:      http.MethodPut,
		Path:        "/labels/{id}",
		Summary:     "Update a label",
		Tags:        []string{"labels"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.UpdateLabel)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-label",
		Method:        http.MethodDelete,
		Path:          "/labels/{id}",
		Summary:       "Delete a label",
		Tags:          []string{"labels"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "labels:write")},
	}, h.DeleteLabel)

	huma.Register(api, huma.Operation{
		OperationID: "list-label-entries",
		Method:      http.MethodGet,
		Path:        "/labels/{id}/entries",
		Summary:     "List entries for a label",
		Tags:        []string{"labels"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListLabelEntries)
}
