package store

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Institution represents a row from the institution_mappings table.
type Institution struct {
	ID                   string    `db:"id"`
	EntityID             string    `db:"entity_id"`
	InstitutionName      string    `db:"institution_name"`
	SourceType           string    `db:"source_type"`
	SettlementWindowDays int       `db:"settlement_window_days"`
	DedupWindowDays      int       `db:"dedup_window_days"`
	AmountTolerancePct   float64   `db:"amount_tolerance_pct"`
	DateCol              string    `db:"date_col"`
	AmountCol            string    `db:"amount_col"`
	MerchantCol          string    `db:"merchant_col"`
	ImportedIDCol        *string   `db:"imported_id_col"`
	BalanceCol           *string   `db:"balance_col"`
	DebitCreditCol       *string   `db:"debit_credit_col"`
	AmountSignConvention string    `db:"amount_sign_convention"`
	CreatedAt            time.Time `db:"created_at"`
}

const institutionCols = `
	id::text, entity_id::text, institution_name, source_type,
	settlement_window_days, dedup_window_days, amount_tolerance_pct,
	date_col, amount_col, merchant_col, imported_id_col,
	balance_col, debit_credit_col, amount_sign_convention, created_at
`

// GetInstitution fetches a single institution by id for an entity.
func (s *Store) GetInstitution(ctx context.Context, entityID, id string) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM institution_mappings
		WHERE entity_id = $1 AND id = $2
	`, institutionCols), entityID, id)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// ListInstitutions returns every institution for an entity, unpaginated.
// Realistic cardinality is a handful, at most a few dozen — not worth pagination.
func (s *Store) ListInstitutions(ctx context.Context, entityID string) ([]Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM institution_mappings
		WHERE entity_id = $1
		ORDER BY institution_name
	`, institutionCols), entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Institution])
}

// CreateInstitution inserts a new institution_mappings row.
func (s *Store) CreateInstitution(ctx context.Context, entityID string, in Institution) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO institution_mappings (
			id, entity_id, institution_name, source_type,
			settlement_window_days, dedup_window_days, amount_tolerance_pct,
			date_col, amount_col, merchant_col, imported_id_col,
			balance_col, debit_credit_col, amount_sign_convention, created_at
		) VALUES (
			gen_random_uuid(), $1, $2, $3,
			$4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, NOW()
		)
		RETURNING %s
	`, institutionCols),
		entityID, in.InstitutionName, in.SourceType,
		in.SettlementWindowDays, in.DedupWindowDays, in.AmountTolerancePct,
		in.DateCol, in.AmountCol, in.MerchantCol, in.ImportedIDCol,
		in.BalanceCol, in.DebitCreditCol, in.AmountSignConvention,
	)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// UpdateInstitution updates mutable fields on an institution_mappings row.
func (s *Store) UpdateInstitution(ctx context.Context, entityID, id string, in Institution) (Institution, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE institution_mappings SET
			institution_name = $3,
			source_type = $4,
			settlement_window_days = $5,
			dedup_window_days = $6,
			amount_tolerance_pct = $7,
			date_col = $8,
			amount_col = $9,
			merchant_col = $10,
			imported_id_col = $11,
			balance_col = $12,
			debit_credit_col = $13,
			amount_sign_convention = $14
		WHERE entity_id = $1 AND id = $2
		RETURNING %s
	`, institutionCols),
		entityID, id,
		in.InstitutionName, in.SourceType,
		in.SettlementWindowDays, in.DedupWindowDays, in.AmountTolerancePct,
		in.DateCol, in.AmountCol, in.MerchantCol, in.ImportedIDCol,
		in.BalanceCol, in.DebitCreditCol, in.AmountSignConvention,
	)
	if err != nil {
		return Institution{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Institution])
}

// DeleteInstitution removes an institution_mappings row.
func (s *Store) DeleteInstitution(ctx context.Context, entityID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM institution_mappings WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// EncodeCursor base64-encodes "id,createdAt" for opaque cursor pagination.
func EncodeCursor(id string, createdAt time.Time) string {
	raw := id + "," + createdAt.UTC().Format(time.RFC3339Nano)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeCursor decodes a cursor string into id and timestamp string.
func decodeCursor(cursor string) (id string, ts string, err error) {
	b, err := base64.StdEncoding.DecodeString(cursor)
	if err != nil {
		return "", "", err
	}
	raw := string(b)
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == ',' {
			return raw[:i], raw[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("invalid cursor")
}
