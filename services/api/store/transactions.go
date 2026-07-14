package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Transaction represents a row from the transactions table.
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
}

const transactionCols = `
	id::text, entity_id::text, account_id::text, import_batch_id::text,
	date, amount_cents, imported_payee, merchant_normalized,
	imported_id, settlement_status, imported_at
`

// GetTransaction fetches a single transaction by id for an entity.
func (s *Store) GetTransaction(ctx context.Context, entityID, id string) (Transaction, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions
		WHERE entity_id = $1 AND id = $2
	`, transactionCols), entityID, id)
	if err != nil {
		return Transaction{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Transaction])
}

// ListTransactions returns a paginated list of transactions for an entity.
func (s *Store) ListTransactions(ctx context.Context, entityID string, limit int, cursor string) ([]Transaction, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM transactions
			WHERE entity_id = $1
			ORDER BY imported_at DESC, id DESC
			LIMIT $2
		`, transactionCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM transactions
		WHERE entity_id = $1
		  AND (imported_at, id::text) < ($2::timestamptz, $3)
		ORDER BY imported_at DESC, id DESC
		LIMIT $4
	`, transactionCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Transaction])
}
