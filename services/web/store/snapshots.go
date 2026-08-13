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
	IncomeRate   float64   `db:"income_rate"`
	SpendRate    float64   `db:"spend_rate"`
	DriftRate    float64   `db:"drift_rate"`
	ComputedAsOf time.Time `db:"computed_as_of"`
}

// GetSnapshotSummary returns aggregated rates for the latest snapshot date.
// excludeJobIDs skips rows written by in-progress engine jobs, giving a stable
// view of the previous completed run during a crawl.
func (s *Store) GetSnapshotSummary(ctx context.Context, entityID string, excludeJobIDs []string) (SnapshotSummary, error) {
	args := []any{entityID}
	jobExclude := ""
	if len(excludeJobIDs) > 0 {
		args = append(args, excludeJobIDs)
		jobExclude = fmt.Sprintf(" AND NOT (s.job_id::text = ANY($%d))", len(args))
		// subquery also needs the same exclusion so MAX(snapshot_date) is stable
	}
	var subExclude string
	if len(excludeJobIDs) > 0 {
		subExclude = fmt.Sprintf(" AND NOT (s2.job_id::text = ANY($%d))", len(args))
	}
	q := fmt.Sprintf(`
		SELECT
			COALESCE(SUM(CASE WHEN e.direction = 'income' THEN s.actual_rate_per_day ELSE 0 END), 0) AS income_rate,
			COALESCE(SUM(CASE WHEN e.direction = 'spend' THEN s.actual_rate_per_day ELSE 0 END), 0) AS spend_rate,
			COALESCE(SUM(s.drift_per_day), 0) AS drift_rate,
			COALESCE(MAX(s.computed_as_of), NOW()::date) AS computed_as_of
		FROM snapshots s
		JOIN entries e ON e.id = s.node_id AND s.node_type = 'entry'
		WHERE s.entity_id = $1
		  %s
		  AND s.snapshot_date = (
			SELECT MAX(s2.snapshot_date) FROM snapshots s2 WHERE s2.entity_id = $1%s
		  )
	`, jobExclude, subExclude)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return SnapshotSummary{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[SnapshotSummary])
}

// SnapshotDaySummary is one calendar day of aggregated income/spend/margin across all entries.
type SnapshotDaySummary struct {
	SnapshotDate time.Time `db:"snapshot_date"`
	IncomeRate   float64   `db:"income_rate"`
	SpendRate    float64   `db:"spend_rate"`
	MarginRate   float64   `db:"margin_rate"`
	DriftRate    float64   `db:"drift_rate"`
}

// ListSnapshotDaySummaries returns per-day aggregate rates for an entity,
// ordered newest-first. before, dateFrom, and dateTo are optional filters.
// excludeJobIDs skips rows written by in-progress engine jobs so the caller
// sees a stable view of the previous completed run during a crawl.
// Pass limit+1 to detect whether more pages exist.
func (s *Store) ListSnapshotDaySummaries(ctx context.Context, entityID string, limit int, before, dateFrom, dateTo *time.Time, excludeJobIDs []string) ([]SnapshotDaySummary, error) {
	const base = `
		SELECT
			s.snapshot_date,
			COALESCE(SUM(CASE WHEN e.direction = 'income' THEN s.actual_rate_per_day ELSE 0 END), 0) AS income_rate,
			COALESCE(SUM(CASE WHEN e.direction = 'spend'  THEN s.actual_rate_per_day ELSE 0 END), 0) AS spend_rate,
			COALESCE(SUM(CASE WHEN e.direction IN ('income','spend') THEN s.actual_rate_per_day ELSE 0 END), 0) AS margin_rate,
			COALESCE(SUM(s.drift_per_day), 0) AS drift_rate
		FROM snapshots s
		JOIN entries e ON e.id = s.node_id AND s.node_type = 'entry'
		WHERE s.entity_id = $1`

	args := []any{entityID}
	extra := ""
	add := func(cond string, v any) {
		args = append(args, v)
		extra += fmt.Sprintf(" AND %s $%d", cond, len(args))
	}
	if before != nil {
		add("s.snapshot_date <", *before)
	}
	if dateFrom != nil {
		add("s.snapshot_date >=", *dateFrom)
	}
	if dateTo != nil {
		add("s.snapshot_date <=", *dateTo)
	}
	if len(excludeJobIDs) > 0 {
		args = append(args, excludeJobIDs)
		extra += fmt.Sprintf(" AND NOT (s.job_id::text = ANY($%d))", len(args))
	}
	var q string
	if limit > 0 {
		args = append(args, limit)
		q = fmt.Sprintf("%s%s GROUP BY s.snapshot_date ORDER BY s.snapshot_date DESC LIMIT $%d", base, extra, len(args))
	} else {
		q = fmt.Sprintf("%s%s GROUP BY s.snapshot_date ORDER BY s.snapshot_date DESC", base, extra)
	}

	dbrows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(dbrows, pgx.RowToStructByName[SnapshotDaySummary])
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
