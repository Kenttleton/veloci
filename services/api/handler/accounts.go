package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/api/middleware"
	"github.com/veloci/api/queue"
	"github.com/veloci/api/response"
	"github.com/veloci/api/store"
)

// AccountsHandler handles account endpoints.
type AccountsHandler struct {
	s *store.Store
}

// NewAccountsHandler creates an AccountsHandler.
func NewAccountsHandler(s *store.Store) *AccountsHandler {
	return &AccountsHandler{s: s}
}

// accountView is the API representation of an account.
type accountView struct {
	ID               string   `json:"id"`
	InstitutionID    *string  `json:"institution_id"`
	Name             string   `json:"name"`
	AccountType      string   `json:"account_type"`
	Status           string   `json:"status"`
	InterestRate     *float64 `json:"interest_rate"`
	BalanceCents     *int64   `json:"balance_cents"`
	CreditLimitCents *int64   `json:"credit_limit_cents"`
	CreatedAt        string   `json:"created_at"`
}

func toAccountView(a store.Account) accountView {
	return accountView{
		ID:               a.ID,
		InstitutionID:    a.InstitutionID,
		Name:             a.Name,
		AccountType:      a.AccountType,
		Status:           a.Status,
		InterestRate:     a.InterestRate,
		BalanceCents:     a.BalanceCents,
		CreditLimitCents: a.CreditLimitCents,
		CreatedAt:        a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

type listAccountsInput struct {
	Cursor string `query:"cursor"`
	Limit  int    `query:"limit" default:"200" minimum:"1" maximum:"200"`
}

type listAccountsOutput struct {
	Body response.Envelope[[]accountView]
}

type createAccountInput struct {
	Body struct {
		Name             string   `json:"name"          required:"true"`
		AccountType      string   `json:"account_type"  required:"true"`
		Status           string   `json:"status"        required:"true"`
		InstitutionID    *string  `json:"institution_id"`
		InterestRate     *float64 `json:"interest_rate"`
		BalanceCents     *int64   `json:"balance_cents"`
		CreditLimitCents *int64   `json:"credit_limit_cents"`
	}
}

type createAccountOutput struct {
	Body response.Envelope[accountView]
}

type getAccountInput struct {
	PathID string `path:"id"`
}

type getAccountOutput struct {
	Body response.Envelope[accountView]
}

type updateAccountInput struct {
	PathID string `path:"id"`
	Body   struct {
		Name             string   `json:"name"          required:"true"`
		AccountType      string   `json:"account_type"  required:"true"`
		Status           string   `json:"status"        required:"true"`
		InstitutionID    *string  `json:"institution_id"`
		InterestRate     *float64 `json:"interest_rate"`
		BalanceCents     *int64   `json:"balance_cents"`
		CreditLimitCents *int64   `json:"credit_limit_cents"`
	}
}

type updateAccountOutput struct {
	Body response.Envelope[accountView]
}

type deleteAccountInput struct {
	PathID string `path:"id"`
}

func (h *AccountsHandler) ListAccounts(ctx context.Context, input *listAccountsInput) (*listAccountsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 200
	}

	items, err := h.s.ListAccounts(ctx, entityID, limit+1, input.Cursor)
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

	out := &listAccountsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *AccountsHandler) CreateAccount(ctx context.Context, input *createAccountInput) (*createAccountOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.CreateAccount(ctx, entityID, store.Account{
		InstitutionID:    input.Body.InstitutionID,
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
	out := &createAccountOutput{}
	out.Body = response.Single(toAccountView(item))
	return out, nil
}

func (h *AccountsHandler) GetAccount(ctx context.Context, input *getAccountInput) (*getAccountOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetAccount(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getAccountOutput{}
	out.Body = response.Single(toAccountView(item))
	return out, nil
}

func (h *AccountsHandler) UpdateAccount(ctx context.Context, input *updateAccountInput) (*updateAccountOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.UpdateAccount(ctx, entityID, input.PathID, store.Account{
		Name:             input.Body.Name,
		AccountType:      input.Body.AccountType,
		Status:           input.Body.Status,
		InstitutionID:    input.Body.InstitutionID,
		InterestRate:     input.Body.InterestRate,
		BalanceCents:     input.Body.BalanceCents,
		CreditLimitCents: input.Body.CreditLimitCents,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &updateAccountOutput{}
	out.Body = response.Single(toAccountView(item))
	return out, nil
}

func (h *AccountsHandler) DeleteAccount(ctx context.Context, input *deleteAccountInput) (*struct{}, error) {
	entityID := middleware.EntityID(ctx)

	err := h.s.DeleteAccount(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	return nil, nil
}

// RegisterAccountsRoutes registers account endpoints on the given Huma API.
func RegisterAccountsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewAccountsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-accounts",
		Method:      http.MethodGet,
		Path:        "/accounts",
		Summary:     "List accounts",
		Tags:        []string{"accounts"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListAccounts)

	huma.Register(api, huma.Operation{
		OperationID: "create-account",
		Method:      http.MethodPost,
		Path:        "/accounts",
		Summary:     "Create an account",
		Tags:        []string{"accounts"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.CreateAccount)

	huma.Register(api, huma.Operation{
		OperationID: "get-account",
		Method:      http.MethodGet,
		Path:        "/accounts/{id}",
		Summary:     "Get an account",
		Tags:        []string{"accounts"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetAccount)

	huma.Register(api, huma.Operation{
		OperationID: "update-account",
		Method:      http.MethodPut,
		Path:        "/accounts/{id}",
		Summary:     "Update an account",
		Tags:        []string{"accounts"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.UpdateAccount)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-account",
		Method:        http.MethodDelete,
		Path:          "/accounts/{id}",
		Summary:       "Delete an account",
		Tags:          []string{"accounts"},
		DefaultStatus: http.StatusNoContent,
		Middlewares:   huma.Middlewares{middleware.RequirePermission(perms, "accounts:write")},
	}, h.DeleteAccount)
}
