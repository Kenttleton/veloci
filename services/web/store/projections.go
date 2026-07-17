package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Projection represents a row from the projections table.
type Projection struct {
	ID                    string    `db:"id"`
	EntityID              string    `db:"entity_id"`
	AccountID             *string   `db:"account_id"`
	JobID                 string    `db:"job_id"`
	ProjectedDate         time.Time `db:"projected_date"`
	IncomeRatePerDay      float64   `db:"income_rate_per_day"`
	CommitmentRatePerDay  float64   `db:"commitment_rate_per_day"`
	MarginRatePerDay      float64   `db:"margin_rate_per_day"`
	ProjectedBalanceCents *int64    `db:"projected_balance_cents"`
	IsPinchPoint          bool      `db:"is_pinch_point"`
}

const projectionCols = `
	id::text, entity_id::text, account_id::text, job_id::text,
	projected_date, income_rate_per_day, commitment_rate_per_day, margin_rate_per_day,
	projected_balance_cents, is_pinch_point
`

// ListProjections returns a paginated list of projections for an entity.
func (s *Store) ListProjections(ctx context.Context, entityID string, limit int, cursor string) ([]Projection, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM projections
			WHERE entity_id = $1
			ORDER BY projected_date DESC, id DESC
			LIMIT $2
		`, projectionCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Projection])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM projections
		WHERE entity_id = $1
		  AND (projected_date, id::text) < ($2::timestamptz, $3)
		ORDER BY projected_date DESC, id DESC
		LIMIT $4
	`, projectionCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Projection])
}
