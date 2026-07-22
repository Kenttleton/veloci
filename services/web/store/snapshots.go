package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Snapshot represents a row from the snapshots table.
type Snapshot struct {
	ID                    string    `db:"id"`
	EntityID              string    `db:"entity_id"`
	NodeID                string    `db:"node_id"`
	NodeType              string    `db:"node_type"`
	SnapshotDate          time.Time `db:"snapshot_date"`
	ComputedAsOf          time.Time `db:"computed_as_of"`
	JobID                 string    `db:"job_id"`
	ActualRatePerDay      float64   `db:"actual_rate_per_day"`
	ProjectedRatePerDay   float64   `db:"projected_rate_per_day"`
	DriftPerDay           float64   `db:"drift_per_day"`
	SlopePerDay           float64   `db:"slope_per_day"`
	RSquared              float64   `db:"r_squared"`
	TransactionCount      int       `db:"transaction_count"`
	WindowDaysUsed        int       `db:"window_days_used"`
	RollingWindowTotalCents int64   `db:"rolling_window_total_cents"`
	BalanceCents          *int64    `db:"balance_cents"`
}

const snapshotCols = `
	id::text, entity_id::text, node_id::text, node_type,
	snapshot_date, computed_as_of, job_id::text,
	actual_rate_per_day, projected_rate_per_day, drift_per_day,
	slope_per_day, r_squared, transaction_count, window_days_used,
	rolling_window_total_cents, balance_cents
`

// ListSnapshots returns a paginated list of snapshots for an entity.
func (s *Store) ListSnapshots(ctx context.Context, entityID string, limit int, cursor string) ([]Snapshot, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM snapshots
			WHERE entity_id = $1
			ORDER BY snapshot_date DESC, id DESC
			LIMIT $2
		`, snapshotCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Snapshot])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM snapshots
		WHERE entity_id = $1
		  AND (snapshot_date, id::text) < ($2::timestamptz, $3)
		ORDER BY snapshot_date DESC, id DESC
		LIMIT $4
	`, snapshotCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Snapshot])
}

// SnapshotSummary holds the aggregate across all nodes for the latest snapshot date.
type SnapshotSummary struct {
	IncomeRate      float64 `db:"income_rate"`
	SpendRate float64 `db:"spend_rate"`
	DriftRate       float64 `db:"drift_rate"`
}

// GetSnapshotSummary returns aggregated rates for the latest snapshot date.
func (s *Store) GetSnapshotSummary(ctx context.Context, entityID string) (SnapshotSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN e.direction = 'income' THEN s.actual_rate_per_day ELSE 0 END), 0) AS income_rate,
			COALESCE(SUM(CASE WHEN e.direction = 'spend' THEN s.actual_rate_per_day ELSE 0 END), 0) AS spend_rate,
			COALESCE(SUM(s.drift_per_day), 0) AS drift_rate
		FROM snapshots s
		JOIN entries e ON e.id = s.node_id AND s.node_type = 'entry'
		WHERE s.entity_id = $1
		  AND s.snapshot_date = (
			SELECT MAX(s2.snapshot_date) FROM snapshots s2 WHERE s2.entity_id = $1
		  )
	`, entityID)
	if err != nil {
		return SnapshotSummary{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[SnapshotSummary])
}

// SnapshotHistoryRow is a snapshot history entry, potentially OHLC-aggregated.
type SnapshotHistoryRow struct {
	Period          time.Time `db:"period"`
	ActualRatePerDay float64  `db:"actual_rate_per_day"`
	OpenRate        *float64  `db:"open_rate"`
	HighRate        *float64  `db:"high_rate"`
	LowRate         *float64  `db:"low_rate"`
	CloseRate       *float64  `db:"close_rate"`
}

// GetSnapshotHistory returns time-series history for a node.
func (s *Store) GetSnapshotHistory(ctx context.Context, entityID, nodeID string, before time.Time, limit int, granularity string) ([]SnapshotHistoryRow, error) {
	if granularity == "day" || granularity == "" {
		rows, err := s.pool.Query(ctx, `
			SELECT
				snapshot_date AS period,
				actual_rate_per_day,
				NULL::float8 AS open_rate,
				NULL::float8 AS high_rate,
				NULL::float8 AS low_rate,
				NULL::float8 AS close_rate
			FROM snapshots
			WHERE entity_id = $1 AND node_id = $2 AND snapshot_date <= $3
			ORDER BY snapshot_date DESC
			LIMIT $4
		`, entityID, nodeID, before, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[SnapshotHistoryRow])
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT
			DATE_TRUNC('%s', snapshot_date) AS period,
			AVG(actual_rate_per_day) AS actual_rate_per_day,
			MAX(actual_rate_per_day) AS open_rate,
			MAX(actual_rate_per_day) AS high_rate,
			MIN(actual_rate_per_day) AS low_rate,
			MIN(actual_rate_per_day) AS close_rate
		FROM snapshots
		WHERE entity_id = $1 AND node_id = $2 AND snapshot_date <= $3
		GROUP BY DATE_TRUNC('%s', snapshot_date)
		ORDER BY period DESC
		LIMIT $4
	`, granularity, granularity), entityID, nodeID, before, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[SnapshotHistoryRow])
}
