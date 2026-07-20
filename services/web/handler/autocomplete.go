package handler

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/store"
)

type AutocompleteHandler struct {
	s *store.Store
}

func NewAutocompleteHandler(s *store.Store) *AutocompleteHandler {
	return &AutocompleteHandler{s: s}
}

type autocompleteOutput struct {
	Body store.AutocompleteData
}

func (h *AutocompleteHandler) Get(ctx context.Context, _ *struct{}) (*autocompleteOutput, error) {
	entityID := middleware.EntityID(ctx)
	data, err := h.s.ListAutocompleteData(ctx, entityID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &autocompleteOutput{}
	out.Body = data
	return out, nil
}

type unaliasedMerchantsInput struct {
	Q string `query:"q"`
}

type unaliasedMerchantsOutput struct {
	Body struct {
		Items []string `json:"items"`
	}
}

func (h *AutocompleteHandler) UnaliasedMerchants(ctx context.Context, input *unaliasedMerchantsInput) (*unaliasedMerchantsOutput, error) {
	entityID := middleware.EntityID(ctx)
	items, err := h.s.ListUnaliasedTransactionMerchants(ctx, entityID, input.Q)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &unaliasedMerchantsOutput{}
	out.Body.Items = items
	return out, nil
}

func RegisterAutocompleteRoutes(api huma.API, s *store.Store, perms middleware.PermissionCache) {
	h := NewAutocompleteHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "get-autocomplete",
		Method:      http.MethodGet,
		Path:        "/autocomplete",
		Summary:     "Get merchant and label names for conditions editor autocomplete",
		Tags:        []string{"autocomplete"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.Get)

	huma.Register(api, huma.Operation{
		OperationID: "list-unaliased-merchants",
		Method:      http.MethodGet,
		Path:        "/transactions/merchant-strings",
		Summary:     "List unaliased transaction merchant strings for alias assignment",
		Tags:        []string{"autocomplete"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.UnaliasedMerchants)
}
