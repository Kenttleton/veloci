package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Transaction represents a row from the transactions table with entry assignments.
type Transaction struct {
	ID                 string    `db:"id"`
	EntityID           string    `db:"entity_id"`
	AccountID          string    `db:"account_id"`
	ImportBatchID      *string   `db:"import_batch_id"`
	Date               time.Time `db:"date"`
	AmountCents        int64     `db:"amount_cents"`
	ImportedPayee      string    `db:"imported_payee"`
	MerchantNormalized string    `db:"merchant_normalized"`
	ImportedID         *string   `db:"imported_id"`
	SettlementStatus   string    `db:"settlement_status"`
	ImportedAt         time.Time `db:"imported_at"`
	EntryIDs           []string  `db:"entry_ids"`
}

const transactionCols = `
	t.id::text, t.entity_id::text, t.account_id::text, t.import_batch_id::text,
	t.date, t.amount_cents, t.imported_payee, t.merchant_normalized,
	t.imported_id, t.settlement_status, t.imported_at,
	COALESCE((
		SELECT array_agg(tea.entry_id::text)
		FROM transaction_entry_assignments tea
		WHERE tea.transaction_id = t.id
	), '{}') AS entry_ids
`

// GetTransaction fetches a single transaction by id for an entity.
func (s *Store) GetTransaction(ctx context.Context, entityID, id string) (Transaction, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions t
		WHERE t.entity_id = $1 AND t.id = $2
	`, transactionCols), entityID, id)
	if err != nil {
		return Transaction{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Transaction])
}

// ListTransactions returns a paginated list of transactions for an entity ordered by date DESC.
// spanDays > 0 limits results to the last N days relative to the entity's latest transaction date.
// accountID and entryID are optional filters; entryID joins through transaction_entry_assignments.
func (s *Store) ListTransactions(ctx context.Context, entityID string, spanDays int, accountID, entryID string, limit int, cursor string) ([]Transaction, error) {
	args := []any{entityID}
	extraFilters := ""

	if spanDays > 0 {
		args = append(args, spanDays)
		extraFilters += fmt.Sprintf(`
			AND t.date >= (
				SELECT COALESCE(MAX(t2.date), CURRENT_DATE) - ($%d || ' days')::interval
				FROM transactions t2 WHERE t2.entity_id = $1
			)`, len(args))
	}
	if accountID != "" {
		args = append(args, accountID)
		extraFilters += fmt.Sprintf(" AND t.account_id = $%d", len(args))
	}
	if entryID != "" {
		args = append(args, entryID)
		extraFilters += fmt.Sprintf(`
			AND EXISTS (
				SELECT 1 FROM transaction_entry_assignments tea
				WHERE tea.transaction_id = t.id AND tea.entry_id = $%d::uuid
			)`, len(args))
	}

	if cursor == "" {
		args = append(args, limit)
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM transactions t
			WHERE t.entity_id = $1%s
			ORDER BY t.date DESC, t.id DESC
			LIMIT $%d
		`, transactionCols, extraFilters, len(args)), args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
	}

	cursorID, cursorDate, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	args = append(args, cursorDate)
	datePos := len(args)
	args = append(args, cursorID)
	idPos := len(args)
	args = append(args, limit)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions t
		WHERE t.entity_id = $1%s
		  AND (t.date, t.id::text) < ($%d::date, $%d)
		ORDER BY t.date DESC, t.id DESC
		LIMIT $%d
	`, transactionCols, extraFilters, datePos, idPos, len(args)), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
}
