package handler

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
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
	ID                   string   `json:"id"`
	InstitutionID        *string  `json:"institution_id"`
	Name                 string   `json:"name"`
	AccountType          string   `json:"account_type"`
	Status               string   `json:"status"`
	InterestRate         *float64 `json:"interest_rate"`
	StartingBalanceCents int64    `json:"starting_balance_cents"`
	BalanceCents         *int64   `json:"balance_cents"`
	CreditLimitCents     *int64   `json:"credit_limit_cents"`
	CreatedAt            string   `json:"created_at"`
}

func toAccountView(a store.Account) accountView {
	return accountView{
		ID:                   a.ID,
		InstitutionID:        a.InstitutionID,
		Name:                 a.Name,
		AccountType:          a.AccountType,
		Status:               a.Status,
		InterestRate:         a.InterestRate,
		StartingBalanceCents: a.StartingBalanceCents,
		BalanceCents:         a.BalanceCents,
		CreditLimitCents:     a.CreditLimitCents,
		CreatedAt:            a.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

func (h *AccountsHandler) ListAccounts(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	limit := 200
	if l, err := strconv.Atoi(c.QueryParam("limit")); err == nil && l > 0 {
		limit = l
	}
	cursor := c.QueryParam("cursor")

	items, err := h.s.ListAccounts(ctx, entityID, limit+1, cursor)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	var nextCursor *string
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		cur := store.EncodeCursor(last.ID, last.CreatedAt)
		nextCursor = &cur
	}

	views := make([]accountView, len(items))
	for i, item := range items {
		views[i] = toAccountView(item)
	}

	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *AccountsHandler) CreateAccount(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		Name                 string   `json:"name"`
		AccountType          string   `json:"account_type"`
		Status               string   `json:"status"`
		InstitutionID        *string  `json:"institution_id"`
		InterestRate         *float64 `json:"interest_rate"`
		StartingBalanceCents int64    `json:"starting_balance_cents"`
		CreditLimitCents     *int64   `json:"credit_limit_cents"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	item, err := h.s.CreateAccount(ctx, entityID, store.Account{
		InstitutionID:        body.InstitutionID,
		Name:                 body.Name,
		AccountType:          body.AccountType,
		Status:               body.Status,
		InterestRate:         body.InterestRate,
		StartingBalanceCents: body.StartingBalanceCents,
		CreditLimitCents:     body.CreditLimitCents,
	})
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, response.Single(toAccountView(item)))
}

func (h *AccountsHandler) GetAccount(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	item, err := h.s.GetAccount(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, response.Single(toAccountView(item)))
}

func (h *AccountsHandler) UpdateAccount(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	var body struct {
		Name                 string   `json:"name"`
		AccountType          string   `json:"account_type"`
		Status               string   `json:"status"`
		InstitutionID        *string  `json:"institution_id"`
		InterestRate         *float64 `json:"interest_rate"`
		StartingBalanceCents int64    `json:"starting_balance_cents"`
		CreditLimitCents     *int64   `json:"credit_limit_cents"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	item, err := h.s.UpdateAccount(ctx, entityID, id, store.Account{
		Name:                 body.Name,
		AccountType:          body.AccountType,
		Status:               body.Status,
		InstitutionID:        body.InstitutionID,
		InterestRate:         body.InterestRate,
		StartingBalanceCents: body.StartingBalanceCents,
		CreditLimitCents:     body.CreditLimitCents,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		slog.ErrorContext(ctx, "UpdateAccount failed", "error", err, "account_id", id, "entity_id", entityID)
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, response.Single(toAccountView(item)))
}

func (h *AccountsHandler) DeleteAccount(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	err := h.s.DeleteAccount(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.NoContent(http.StatusNoContent)
}

// RegisterAccountsRoutes registers account endpoints on the given Echo group.
func RegisterAccountsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewAccountsHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	write := g.Group("", middleware.RequirePermission(perms, "accounts:write"))

	read.GET("/accounts", h.ListAccounts)
	write.POST("/accounts", h.CreateAccount)
	read.GET("/accounts/:id", h.GetAccount)
	write.PUT("/accounts/:id", h.UpdateAccount)
	write.DELETE("/accounts/:id", h.DeleteAccount)
}
