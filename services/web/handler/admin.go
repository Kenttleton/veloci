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

type adminStatusOutput struct {
	Body response.Envelope[adminStatusView]
}

type adminEntitiesOutput struct {
	Body response.Envelope[[]adminEntityView]
}

func requireServerAdmin(ctx context.Context) error {
	if middleware.SystemRole(ctx) != "server_admin" {
		return huma.Error403Forbidden("forbidden")
	}
	return nil
}

func (h *AdminHandler) GetStatus(ctx context.Context, _ *struct{}) (*adminStatusOutput, error) {
	if err := requireServerAdmin(ctx); err != nil {
		return nil, err
	}
	out := &adminStatusOutput{}
	out.Body = response.Single(adminStatusView{
		Status:    "ok",
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05Z07:00"),
	})
	return out, nil
}

func (h *AdminHandler) ListEntities(ctx context.Context, _ *struct{}) (*adminEntitiesOutput, error) {
	if err := requireServerAdmin(ctx); err != nil {
		return nil, err
	}

	entities, err := h.s.ListEntities(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	views := make([]adminEntityView, len(entities))
	for i, e := range entities {
		views[i] = adminEntityView{EntityID: e.EntityID, UserCount: e.UserCount}
	}

	out := &adminEntitiesOutput{}
	out.Body = response.Single(views)
	return out, nil
}

// RegisterAdminRoutes registers admin endpoints on the given Huma API.
func RegisterAdminRoutes(api huma.API, s *store.Store, _ *queue.Publisher, _ middleware.PermissionCache) {
	h := NewAdminHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "admin-status",
		Method:      http.MethodGet,
		Path:        "/admin/status",
		Summary:     "Get service status (server_admin only)",
		Tags:        []string{"admin"},
	}, h.GetStatus)

	huma.Register(api, huma.Operation{
		OperationID: "admin-entities",
		Method:      http.MethodGet,
		Path:        "/admin/entities",
		Summary:     "List all entities (server_admin only)",
		Tags:        []string{"admin"},
	}, h.ListEntities)
}
