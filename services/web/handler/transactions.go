package handler

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/labstack/echo/v4"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/response"
	"github.com/veloci/veloci/store"
)

// TransactionsHandler handles transaction endpoints.
type TransactionsHandler struct {
	s *store.Store
}

// NewTransactionsHandler creates a TransactionsHandler.
func NewTransactionsHandler(s *store.Store) *TransactionsHandler {
	return &TransactionsHandler{s: s}
}

// transactionView is the API representation of a transaction.
type transactionView struct {
	ID                 string   `json:"id"`
	AccountID          string   `json:"account_id"`
	ImportBatchID      *string  `json:"import_batch_id"`
	Date               string   `json:"date"`
	AmountCents        int64    `json:"amount_cents"`
	ImportedPayee      string   `json:"imported_payee"`
	MerchantNormalized string   `json:"merchant_normalized"`
	ImportedID         *string  `json:"imported_id"`
	SettlementStatus   string   `json:"settlement_status"`
	ImportedAt         string   `json:"imported_at"`
	EntryIDs           []string `json:"entry_ids"`
}

func toTransactionView(t store.Transaction) transactionView {
	entryIDs := t.EntryIDs
	if entryIDs == nil {
		entryIDs = []string{}
	}
	return transactionView{
		ID:                 t.ID,
		AccountID:          t.AccountID,
		ImportBatchID:      t.ImportBatchID,
		Date:               t.Date.Format("2006-01-02"),
		AmountCents:        t.AmountCents,
		ImportedPayee:      t.ImportedPayee,
		MerchantNormalized: t.MerchantNormalized,
		ImportedID:         t.ImportedID,
		SettlementStatus:   t.SettlementStatus,
		ImportedAt:         t.ImportedAt.Format("2006-01-02T15:04:05Z07:00"),
		EntryIDs:           entryIDs,
	}
}

func (h *TransactionsHandler) ListTransactions(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	dateFrom := c.QueryParam("date_from")
	dateTo := c.QueryParam("date_to")
	spanDays, _ := strconv.Atoi(c.QueryParam("span_days"))
	spanMonths, _ := strconv.Atoi(c.QueryParam("span_months"))
	spanYears, _ := strconv.Atoi(c.QueryParam("span_years"))
	accountID := c.QueryParam("account_id")
	entryID := c.QueryParam("entry_id")
	cursor := c.QueryParam("cursor")
	limit, err := strconv.Atoi(c.QueryParam("limit"))
	if err != nil || limit <= 0 {
		limit = 200
	}

	dr := store.ResolveRange(dateFrom, dateTo, spanDays, spanMonths, spanYears)
	items, err := h.s.ListTransactions(ctx, entityID, dr, accountID, entryID, limit+1, cursor)
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
		c := store.EncodeDateCursor(last.ID, last.Date)
		nextCursor = &c
	}

	views := make([]transactionView, len(items))
	for i, item := range items {
		views[i] = toTransactionView(item)
	}
	return c.JSON(http.StatusOK, response.Page(views, nextCursor, limit, hasMore))
}

func (h *TransactionsHandler) GetTransaction(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)
	id := c.Param("id")

	item, err := h.s.GetTransaction(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return echo.NewHTTPError(http.StatusNotFound, "not found")
	}
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	return c.JSON(http.StatusOK, response.Single(toTransactionView(item)))
}

func (h *TransactionsHandler) QueryMerchants(c echo.Context) error {
	ctx := c.Request().Context()
	entityID := middleware.EntityID(ctx)

	var body struct {
		Payee string `json:"payee"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	results, err := h.s.SearchMerchants(ctx, entityID, body.Payee)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}
	if results == nil {
		results = []string{}
	}
	return c.JSON(http.StatusOK, response.Single(results))
}

// RegisterTransactionsRoutes registers transaction endpoints on the given Echo group.
func RegisterTransactionsRoutes(g *echo.Group, s *store.Store, perms middleware.PermissionCache) {
	h := NewTransactionsHandler(s)

	read := g.Group("", middleware.RequirePermission(perms, "accounts:read"))
	read.GET("/transactions", h.ListTransactions)
	read.GET("/transactions/:id", h.GetTransaction)
	read.Add("QUERY", "/transactions/merchant", h.QueryMerchants)
}
