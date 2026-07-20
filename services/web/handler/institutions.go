package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/veloci/fieldregistry"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// InstitutionsHandler handles institution mapping endpoints.
type InstitutionsHandler struct {
	s *store.Store
}

// NewInstitutionsHandler creates an InstitutionsHandler.
func NewInstitutionsHandler(s *store.Store) *InstitutionsHandler {
	return &InstitutionsHandler{s: s}
}

// institutionView is the API representation of an institution mapping.
type institutionView struct {
	ID                   string          `json:"id"`
	InstitutionName      string          `json:"institution_name"`
	SourceType           string          `json:"source_type"`
	SettlementWindowDays int             `json:"settlement_window_days"`
	DedupWindowDays      int             `json:"dedup_window_days"`
	AmountTolerancePct   float64         `json:"amount_tolerance_pct"`
	MappingConfig        json.RawMessage `json:"mapping_config"`
	CreatedAt            string          `json:"created_at"`
}

func toInstitutionView(i store.Institution) institutionView {
	return institutionView{
		ID:                   i.ID,
		InstitutionName:      i.InstitutionName,
		SourceType:           i.SourceType,
		SettlementWindowDays: i.SettlementWindowDays,
		DedupWindowDays:      i.DedupWindowDays,
		AmountTolerancePct:   i.AmountTolerancePct,
		MappingConfig:        i.MappingConfig,
		CreatedAt:            i.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type listInstitutionsInput struct{}

type listInstitutionsOutput struct {
	Body response.Envelope[[]institutionView]
}

type getInstitutionInput struct {
	PathID string `path:"id"`
}

type getInstitutionOutput struct {
	Body response.Envelope[institutionView]
}

// mappingConfigBody is used in create/update request bodies.
type mappingConfigBody struct {
	Layout string            `json:"layout"`
	Fields map[string]string `json:"fields"`
}

type createInstitutionInput struct {
	Body struct {
		InstitutionName      string            `json:"institution_name"       required:"true"`
		SourceType           string            `json:"source_type"            required:"true"`
		SettlementWindowDays int               `json:"settlement_window_days"`
		DedupWindowDays      int               `json:"dedup_window_days"`
		AmountTolerancePct   float64           `json:"amount_tolerance_pct"`
		MappingConfig        mappingConfigBody  `json:"mapping_config"`
	}
}

type createInstitutionOutput struct {
	Body response.Envelope[institutionView]
}

type updateInstitutionInput struct {
	PathID string `path:"id"`
	Body   struct {
		InstitutionName      string            `json:"institution_name"       required:"true"`
		SourceType           string            `json:"source_type"            required:"true"`
		SettlementWindowDays int               `json:"settlement_window_days"`
		DedupWindowDays      int               `json:"dedup_window_days"`
		AmountTolerancePct   float64           `json:"amount_tolerance_pct"`
		MappingConfig        mappingConfigBody  `json:"mapping_config"`
	}
}

type updateInstitutionOutput struct {
	Body response.Envelope[institutionView]
}

type deleteInstitutionInput struct {
	PathID string `path:"id"`
}

type listInstitutionAccountsInput struct {
	PathID string `path:"id"`
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"50" minimum:"1" maximum:"200"`
}

type listInstitutionAccountsOutput struct {
	Body response.Envelope[[]accountView]
}

type createInstitutionAccountInput struct {
	PathID string `path:"id"`
	Body   struct {
		Name             string   `json:"name"          required:"true"`
		AccountType      string   `json:"account_type"  required:"true"`
		Status           string   `json:"status"        required:"true"`
		InterestRate     *float64 `json:"interest_rate"`
		BalanceCents     *int64   `json:"balance_cents"`
		CreditLimitCents *int64   `json:"credit_limit_cents"`
	}
}

type createInstitutionAccountOutput struct {
	Body response.Envelope[accountView]
}

func institutionFromInput(entityID string, b struct {
	InstitutionName      string
	SourceType           string
	SettlementWindowDays int
	DedupWindowDays      int
	AmountTolerancePct   float64
	MappingConfig        mappingConfigBody
}) (store.Institution, error) {
	cfg := fieldregistry.MappingConfig{
		Layout: b.MappingConfig.Layout,
		Fields: b.MappingConfig.Fields,
	}
	if err := fieldregistry.ValidateConfig(b.SourceType, cfg); err != nil {
		return store.Institution{}, err
	}
	cfgJSON, _ := json.Marshal(cfg)

	settlement := b.SettlementWindowDays
	if settlement == 0 {
		settlement = 14
	}
	dedup := b.DedupWindowDays
	if dedup == 0 {
		dedup = 3
	}
	tolerance := b.AmountTolerancePct
	if tolerance == 0 {
		tolerance = 0.005
	}

	return store.Institution{
		EntityID:             entityID,
		InstitutionName:      b.InstitutionName,
		SourceType:           b.SourceType,
		SettlementWindowDays: settlement,
		DedupWindowDays:      dedup,
		AmountTolerancePct:   tolerance,
		MappingConfig:        cfgJSON,
	}, nil
}

// fieldRegistryOutput is the response type for GET /field-registry.
type fieldRegistryOutput struct {
	Body any
}

func (h *InstitutionsHandler) ListInstitutions(ctx context.Context, _ *listInstitutionsInput) (*listInstitutionsOutput, error) {
	entityID := middleware.EntityID(ctx)

	items, err := h.s.ListInstitutions(ctx, entityID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	views := make([]institutionView, len(items))
	for i, item := range items {
		views[i] = toInstitutionView(item)
	}
	out := &listInstitutionsOutput{}
	out.Body = response.Single(views)
	return out, nil
}

func (h *InstitutionsHandler) GetInstitution(ctx context.Context, input *getInstitutionInput) (*getInstitutionOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetInstitution(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getInstitutionOutput{}
	out.Body = response.Single(toInstitutionView(item))
	return out, nil
}

func (h *InstitutionsHandler) CreateInstitution(ctx context.Context, input *createInstitutionInput) (*createInstitutionOutput, error) {
	entityID := middleware.EntityID(ctx)

	inst, err := institutionFromInput(entityID, struct {
		InstitutionName      string
		SourceType           string
		SettlementWindowDays int
		DedupWindowDays      int
		AmountTolerancePct   float64
		MappingConfig        mappingConfigBody
	}{
		input.Body.InstitutionName, input.Body.SourceType,
		input.Body.SettlementWindowDays, input.Body.DedupWindowDays, input.Body.AmountTolerancePct,
		input.Body.MappingConfig,
	})
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	item, err := h.s.CreateInstitution(ctx, entityID, inst)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, huma.Error409Conflict("an institution with this name already exists")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createInstitutionOutput{}
	out.Body = response.Single(toInstitutionView(item))
	return out, nil
}

func (h *InstitutionsHandler) UpdateInstitution(ctx context.Context, input *updateInstitutionInput) (*updateInstitutionOutput, error) {
	entityID := middleware.EntityID(ctx)

	inst, err := institutionFromInput(entityID, struct {
		InstitutionName      string
		SourceType           string
		SettlementWindowDays int
		DedupWindowDays      int
		AmountTolerancePct   float64
		MappingConfig        mappingConfigBody
	}{
		input.Body.InstitutionName, input.Body.SourceType,
		input.Body.SettlementWindowDays, input.Body.DedupWindowDays, input.Body.AmountTolerancePct,
		input.Body.MappingConfig,
	})
	if err != nil {
		return nil, huma.Error422UnprocessableEntity(err.Error())
	}

	item, err := h.s.UpdateInstitution(ctx, entityID, input.PathID, inst)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateInstitutionOutput{}
	out.Body = response.Single(toInstitutionView(item))
	return out, nil
}

func (h *InstitutionsHandler) DeleteInstitution(ctx context.Context, input *deleteInstitutionInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	err := h.s.DeleteInstitution(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

func (h *InstitutionsHandler) ListInstitutionAccounts(ctx context.Context, input *listInstitutionAccountsInput) (*listInstitutionAccountsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	items, err := h.s.ListAccountsByInstitution(ctx, entityID, input.PathID, limit+1, input.Cursor)
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

	views := make([]accountView, len(items))
	for i, item := range items {
		views[i] = toAccountView(item)
	}
	out := &listInstitutionAccountsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *InstitutionsHandler) CreateInstitutionAccount(ctx context.Context, input *createInstitutionAccountInput) (*createInstitutionAccountOutput, error) {
	entityID := middleware.EntityID(ctx)

	institutionID := input.PathID
	item, err := h.s.CreateAccount(ctx, entityID, store.Account{
		InstitutionID:    &institutionID,
		Name:             input.Body.Name,
		AccountType:      input.Body.AccountType,
		Status:           input.Body.Status,
		InterestRate:     input.Body.InterestRate,
		BalanceCents:     input.Body.BalanceCents,
		CreditLimitCents: input.Body.CreditLimitCents,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &createInstitutionAccountOutput{}
	out.Body = response.Single(toAccountView(item))
	return out, nil
}

// RegisterInstitutionsRoutes registers institution endpoints on the given Huma API.
func RegisterInstitutionsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewInstitutionsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "get-field-registry",
		Method:      http.MethodGet,
		Path:        "/field-registry",
		Summary:     "Get the static field registry for CSV layout schemas",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, func(ctx context.Context, _ *struct{}) (*fieldRegistryOutput, error) {
		return &fieldRegistryOutput{Body: fieldregistry.Registry}, nil
	})

	huma.Register(api, huma.Operation{
		OperationID: "list-institutions",
		Method:      http.MethodGet,
		Path:        "/institutions",
		Summary:     "List institution mappings",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListInstitutions)

	huma.Register(api, huma.Operation{
		OperationID: "create-institution",
		Method:      http.MethodPost,
		Path:        "/institutions",
		Summary:     "Create an institution mapping",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.CreateInstitution)

	huma.Register(api, huma.Operation{
		OperationID: "get-institution",
		Method:      http.MethodGet,
		Path:        "/institutions/{id}",
		Summary:     "Get an institution mapping",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetInstitution)

	huma.Register(api, huma.Operation{
		OperationID: "update-institution",
		Method:      http.MethodPut,
		Path:        "/institutions/{id}",
		Summary:     "Update an institution mapping",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.UpdateInstitution)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-institution",
		Method:        http.MethodDelete,
		Path:          "/institutions/{id}",
		Summary:       "Delete an institution mapping",
		Tags:          []string{"institutions"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.DeleteInstitution)

	huma.Register(api, huma.Operation{
		OperationID: "list-institution-accounts",
		Method:      http.MethodGet,
		Path:        "/institutions/{id}/accounts",
		Summary:     "List accounts for an institution",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListInstitutionAccounts)

	huma.Register(api, huma.Operation{
		OperationID: "create-institution-account",
		Method:      http.MethodPost,
		Path:        "/institutions/{id}/accounts",
		Summary:     "Create an account under an institution",
		Tags:        []string{"institutions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.CreateInstitutionAccount)
}
