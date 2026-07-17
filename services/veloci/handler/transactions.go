package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/veloci/middleware"
	"github.com/veloci/veloci/queue"
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

type listTransactionsInput struct {
	DateFrom   string `query:"date_from"`
	DateTo     string `query:"date_to"`
	SpanDays   int    `query:"span_days"   minimum:"0"`
	SpanMonths int    `query:"span_months" minimum:"0"`
	SpanYears  int    `query:"span_years"  minimum:"0"`
	AccountID  string `query:"account_id"`
	EntryID    string `query:"entry_id"`
	Cursor     string `query:"cursor"`
	Limit      int    `query:"limit" default:"200" minimum:"1" maximum:"500"`
}

type listTransactionsOutput struct {
	Body response.Envelope[[]transactionView]
}

type getTransactionInput struct {
	PathID string `path:"id"`
}

type getTransactionOutput struct {
	Body response.Envelope[transactionView]
}

func (h *TransactionsHandler) ListTransactions(ctx context.Context, input *listTransactionsInput) (*listTransactionsOutput, error) {
	entityID := middleware.EntityID(ctx)
	limit := input.Limit
	if limit == 0 {
		limit = 50
	}

	dr := store.ResolveRange(input.DateFrom, input.DateTo, input.SpanDays, input.SpanMonths, input.SpanYears)
	items, err := h.s.ListTransactions(ctx, entityID, dr, input.AccountID, input.EntryID, limit+1, input.Cursor)
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
		c := store.EncodeDateCursor(last.ID, last.Date)
		nextCursor = &c
	}

	views := make([]transactionView, len(items))
	for i, item := range items {
		views[i] = toTransactionView(item)
	}
	out := &listTransactionsOutput{}
	out.Body = response.Page(views, nextCursor, limit, hasMore)
	return out, nil
}

func (h *TransactionsHandler) GetTransaction(ctx context.Context, input *getTransactionInput) (*getTransactionOutput, error) {
	entityID := middleware.EntityID(ctx)

	item, err := h.s.GetTransaction(ctx, entityID, input.PathID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, huma.Error404NotFound("not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &getTransactionOutput{}
	out.Body = response.Single(toTransactionView(item))
	return out, nil
}

// RegisterTransactionsRoutes registers transaction endpoints on the given Huma API.
func RegisterTransactionsRoutes(api huma.API, s *store.Store, _ *queue.Publisher, perms middleware.PermissionCache) {
	h := NewTransactionsHandler(s)

	huma.Register(api, huma.Operation{
		OperationID: "list-transactions",
		Method:      http.MethodGet,
		Path:        "/transactions",
		Summary:     "List transactions",
		Tags:        []string{"transactions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.ListTransactions)

	huma.Register(api, huma.Operation{
		OperationID: "get-transaction",
		Method:      http.MethodGet,
		Path:        "/transactions/{id}",
		Summary:     "Get a transaction",
		Tags:        []string{"transactions"},
		Middlewares: huma.Middlewares{middleware.RequirePermission(perms, "accounts:read")},
	}, h.GetTransaction)
}
