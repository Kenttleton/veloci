package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

// ClassificationsHandler handles classification endpoints.
type ClassificationsHandler struct {
	s *store.Store
}

// NewClassificationsHandler creates a ClassificationsHandler.
func NewClassificationsHandler(s *store.Store) *ClassificationsHandler {
	return &ClassificationsHandler{s: s}
}

// classificationView is the API representation of a classification.
type classificationView struct {
	ID         string          `json:"id"`
	LabelID    string          `json:"label_id"`
	LabelName  *string         `json:"label_name"`
	Conditions json.RawMessage `json:"conditions"`
	Priority   int             `json:"priority"`
	Status     string          `json:"status"`
	Source     string          `json:"source"`
	CreatedAt  string          `json:"created_at"`
}

func toClassificationView(c store.Classification) classificationView {
	return classificationView{
		ID:         c.ID,
		LabelID:    c.LabelID,
		LabelName:  c.LabelName,
		Conditions: c.Conditions,
		Priority:   c.Priority,
		Status:     c.Status,
		Source:     c.Source,
		CreatedAt:  c.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type listClassificationsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listClassificationsOutput struct {
	Body response.Envelope[[]classificationView]
}

type getClassificationInput struct {
	PathID string `path:"id"`
}

type getClassificationOutput struct {
	Body response.Envelope[classificationView]
}

type createClassificationInput struct {
	Body struct {
		LabelID    string          `json:"label_id"   required:"true"`
		Conditions json.RawMessage `json:"conditions" required:"true"`
		Priority   int             `json:"priority"`
		Source     string          `json:"source"`
	}
}

type createClassificationOutput struct {
	Body response.Envelope[classificationView]
}

type updateClassificationInput struct {
	PathID string `path:"id"`
	Body   struct {
		LabelID    string          `json:"label_id"   required:"true"`
		Conditions json.RawMessage `json:"conditions" required:"true"`
		Priority   int             `json:"priority"`
		Status     string          `json:"status" required:"true"`
	}
}

type updateClassificationOutput struct {
	Body response.Envelope[classificationView]
}

type deleteClassificationInput struct {
	PathID string `path:"id"`
}

func (h *ClassificationsHandler) ListClassifications(ctx context.Context, input *listClassificationsInput) (*listClassificationsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListClassifications(ctx, entityID, limit+1, input.Cursor)
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

	views := make([]classificationView, len(items))
	for i, item := range items {
		views[i] = toClassificationView(item)
	}
	out := &listClassificationsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *ClassificationsHandler) GetClassification(ctx context.Context, input *getClassificationInput) (*getClassificationOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetClassification(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getClassificationOutput{}
	out.Body = response.Single(toClassificationView(item))
	return out, nil
}

func (h *ClassificationsHandler) CreateClassification(ctx context.Context, input *createClassificationInput) (*createClassificationOutput, error) {
	entityID := middleware.EntityID(ctx)

	source := input.Body.Source
	if source == "" {
		source = "user"
	}

	item, err := h.s.CreateClassification(ctx, entityID, store.CreateClassificationInput{
		LabelID:    input.Body.LabelID,
		Conditions: input.Body.Conditions,
		Priority:   input.Body.Priority,
		Source:     source,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createClassificationOutput{}
	out.Body = response.Single(toClassificationView(item))
	return out, nil
}

func (h *ClassificationsHandler) UpdateClassification(ctx context.Context, input *updateClassificationInput) (*updateClassificationOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.UpdateClassification(ctx, entityID, input.PathID, store.UpdateClassificationInput{
		LabelID:    input.Body.LabelID,
		Conditions: input.Body.Conditions,
		Priority:   input.Body.Priority,
		Status:     input.Body.Status,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateClassificationOutput{}
	out.Body = response.Single(toClassificationView(item))
	return out, nil
}

func (h *ClassificationsHandler) DeleteClassification(ctx context.Context, input *deleteClassificationInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	err := h.s.DeleteClassification(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

// RegisterClassificationsRoutes registers classification endpoints on the given Huma API.
func RegisterClassificationsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewClassificationsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-classifications",
		Method:      http.MethodGet,
		Path:        "/classifications",
		Summary:     "List classifications",
		Tags:        []string{"classifications"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListClassifications)

	huma.Register(api, huma.Operation{
		OperationID: "create-classification",
		Method:      http.MethodPost,
		Path:        "/classifications",
		Summary:     "Create a classification",
		Tags:        []string{"classifications"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "classifications:write")},
	}, h.CreateClassification)

	huma.Register(api, huma.Operation{
		OperationID: "get-classification",
		Method:      http.MethodGet,
		Path:        "/classifications/{id}",
		Summary:     "Get a classification",
		Tags:        []string{"classifications"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetClassification)

	huma.Register(api, huma.Operation{
		OperationID: "update-classification",
		Method:      http.MethodPut,
		Path:        "/classifications/{id}",
		Summary:     "Update a classification",
		Tags:        []string{"classifications"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "classifications:write")},
	}, h.UpdateClassification)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-classification",
		Method:        http.MethodDelete,
		Path:          "/classifications/{id}",
		Summary:       "Delete a classification",
		Tags:          []string{"classifications"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "classifications:write")},
	}, h.DeleteClassification)
}
