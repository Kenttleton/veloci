package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EntryRow is the full entries row joined with optional label name and latest snapshot.
type EntryRow struct {
	ID                  string          `db:"id"`
	EntityID            string          `db:"entity_id"`
	LabelID             *string         `db:"label_id"`
	LabelName           *string         `db:"label_name"`
	Direction           string          `db:"direction"`
	EntryType           string          `db:"entry_type"`
	PeriodDays          int             `db:"period_days"`
	VariableMethod      *string         `db:"variable_method"`
	ProjectedRatePerDay *float64        `db:"projected_rate_per_day"`
	Conditions          json.RawMessage `db:"conditions"`
	Priority            int             `db:"priority"`
	Status              string          `db:"status"`
	Source              string          `db:"source"`
	RecurrenceAnchor    *string         `db:"recurrence_anchor"`
	NextDueDate         *time.Time      `db:"next_due_date"`
	ProjectTentatively  bool            `db:"project_tentatively"`
	PendingAmountCents  *int64          `db:"pending_amount_cents"`
	PendingEffectiveDate *time.Time     `db:"pending_effective_date"`
	StartDate           time.Time       `db:"start_date"`
	EndDate             *time.Time      `db:"end_date"`
	CreatedAt           time.Time       `db:"created_at"`
	// Engine review metadata (NULL for user-created entries)
	AlertType                *string    `db:"alert_type"`
	Fitness                  *float64   `db:"fitness"`
	MerchantFit              *float64   `db:"merchant_fit"`
	TimingFit                *float64   `db:"timing_fit"`
	AmountFit                *float64   `db:"amount_fit"`
	SampleMerchants          []string   `db:"sample_merchants"`
	MatchedTransactionCount  *int       `db:"matched_transaction_count"`
	ReviewedBy               *string    `db:"reviewed_by"`
	ReviewedAt               *time.Time `db:"reviewed_at"`
	// From latest snapshot join (nullable when no snapshot exists yet)
	ActualRatePerDay    *float64 `db:"actual_rate_per_day"`
	SnapshotDriftPerDay *float64 `db:"drift_per_day"`
}

const entryCols = `
	e.id::text, e.entity_id::text, e.label_id::text, l.name AS label_name,
	e.direction, e.entry_type, e.period_days, e.variable_method,
	e.projected_rate_per_day, e.conditions, e.priority, e.status, e.source,
	e.recurrence_anchor, e.next_due_date, e.project_tentatively,
	e.pending_amount_cents, e.pending_effective_date,
	e.start_date, e.end_date, e.created_at,
	e.alert_type, e.fitness, e.merchant_fit, e.timing_fit,
	e.amount_fit, e.sample_merchants, e.matched_transaction_count,
	e.reviewed_by::text, e.reviewed_at,
	s.actual_rate_per_day, s.drift_per_day
`

// ListEntries returns a paginated list of entries ordered by start_date DESC.
// dr filters on start_date; see DateRange / ResolveRange for resolution rules.
// accountID limits to entries with transactions in that account.
// statusFilter defaults to active-only; pass "all" for every status.
func (s *Store) ListEntries(ctx context.Context, entityID string, dr DateRange, accountID, statusFilter string, limit int, cursor string) ([]EntryRow, error) {
	statusCond := `e.status = 'active'`
	switch statusFilter {
	case "all":
		statusCond = `1=1`
	case "pending_review", "inactive":
		statusCond = `e.status = '` + statusFilter + `'`
	}

	args := []any{entityID}
	extraFilters := ""

	if dr.From != "" {
		args = append(args, dr.From)
		extraFilters += fmt.Sprintf(" AND e.start_date >= $%d::date", len(args))
	}
	if dr.To != "" {
		args = append(args, dr.To)
		extraFilters += fmt.Sprintf(" AND e.start_date <= $%d::date", len(args))
	}
	if dr.SpanInterval != "" {
		args = append(args, dr.SpanInterval)
		extraFilters += fmt.Sprintf(`
			AND e.start_date >= (
				SELECT COALESCE(MAX(e2.start_date), CURRENT_DATE) - $%d::interval
				FROM entries e2 WHERE e2.entity_id = $1
			)`, len(args))
	}
	if accountID != "" {
		args = append(args, accountID)
		extraFilters += fmt.Sprintf(`
			AND EXISTS (
				SELECT 1 FROM transaction_entry_assignments tea
				JOIN transactions t ON t.id = tea.transaction_id
				WHERE tea.entry_id = e.id AND t.account_id = $%d
			)`, len(args))
	}

	const entryFrom = `
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
	`

	if cursor == "" {
		args = append(args, limit)
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s %s
			WHERE e.entity_id = $1 AND %s%s
			ORDER BY e.start_date DESC, e.id DESC
			LIMIT $%d
		`, entryCols, entryFrom, statusCond, extraFilters, len(args)), args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
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
		SELECT %s %s
		WHERE e.entity_id = $1 AND %s%s
		  AND (e.start_date, e.id::text) < ($%d::date, $%d)
		ORDER BY e.start_date DESC, e.id DESC
		LIMIT $%d
	`, entryCols, entryFrom, statusCond, extraFilters, datePos, idPos, len(args)), args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
}

// GetEntry fetches a single entry with budget-view fields.
func (s *Store) GetEntry(ctx context.Context, entityID, id string) (EntryRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
		WHERE e.entity_id = $1 AND e.id = $2
	`, entryCols), entityID, id)
	if err != nil {
		return EntryRow{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[EntryRow])
}

// CreateEntryInput holds the fields needed to insert an entry.
type CreateEntryInput struct {
	LabelID              *string
	Direction            string
	EntryType            string
	PeriodDays           int
	VariableMethod       *string
	ProjectedRatePerDay  *float64
	Conditions           json.RawMessage
	Priority             int
	Source               string
	ProjectTentatively   bool
	StartDate            time.Time
	EndDate              *time.Time
}

// CreateEntry inserts a new entry row with status='pending_review'.
func (s *Store) CreateEntry(ctx context.Context, entityID string, in CreateEntryInput) (EntryRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO entries (
			id, entity_id, label_id, direction, entry_type, period_days,
			variable_method, projected_rate_per_day, conditions, priority,
			status, source, project_tentatively, start_date, end_date, created_at
		) VALUES (
			gen_random_uuid(), $1, $2::uuid, $3, $4, $5,
			$6, $7, $8, $9,
			'pending_review', $10, $11, $12, $13, NOW()
		)
		RETURNING %s,
		NULL::text AS alert_type, NULL::numeric AS fitness,
		NULL::numeric AS merchant_fit, NULL::numeric AS timing_fit,
		NULL::numeric AS amount_fit, NULL::text[] AS sample_merchants,
		NULL::int AS matched_transaction_count, NULL::text AS reviewed_by, NULL::timestamptz AS reviewed_at,
		NULL::float8 AS actual_rate_per_day, NULL::float8 AS drift_per_day
	`, `
		id::text, entity_id::text, label_id::text,
		NULL AS label_name, direction, entry_type, period_days,
		variable_method, projected_rate_per_day, conditions, priority, status, source,
		recurrence_anchor, next_due_date, project_tentatively,
		pending_amount_cents, pending_effective_date,
		start_date, end_date, created_at
	`),
		entityID, in.LabelID, in.Direction, in.EntryType, in.PeriodDays,
		in.VariableMethod, in.ProjectedRatePerDay, in.Conditions, in.Priority,
		in.Source, in.ProjectTentatively, in.StartDate, in.EndDate,
	)
	if err != nil {
		return EntryRow{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[EntryRow])
}

// UpdateEntryInput holds the mutable fields for an entry update.
type UpdateEntryInput struct {
	LabelID             *string
	Direction           string
	EntryType           string
	PeriodDays          int
	VariableMethod      *string
	ProjectedRatePerDay *float64
	Conditions          json.RawMessage
	Priority            int
	Status              string
	ProjectTentatively  bool
	StartDate           time.Time
	EndDate             *time.Time
}

// UpdateEntry updates mutable fields on an entry. When label_id changes, the
// previous label is cleaned up if it has no remaining entry associations.
func (s *Store) UpdateEntry(ctx context.Context, entityID, id string, in UpdateEntryInput) (EntryRow, error) {
	var oldLabelID *string
	_ = s.pool.QueryRow(ctx, `
		SELECT label_id::text FROM entries WHERE entity_id = $1 AND id = $2
	`, entityID, id).Scan(&oldLabelID)

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE entries SET
			label_id = $3::uuid,
			direction = $4,
			entry_type = $5,
			period_days = $6,
			variable_method = $7,
			projected_rate_per_day = $8,
			conditions = $9,
			priority = $10,
			status = $11,
			project_tentatively = $12,
			start_date = $13,
			end_date = $14
		WHERE entity_id = $1 AND id = $2
		RETURNING %s,
		NULL::text AS alert_type, NULL::numeric AS fitness,
		NULL::numeric AS merchant_fit, NULL::numeric AS timing_fit,
		NULL::numeric AS amount_fit, NULL::text[] AS sample_merchants,
		NULL::int AS matched_transaction_count, NULL::text AS reviewed_by, NULL::timestamptz AS reviewed_at,
		NULL::float8 AS actual_rate_per_day, NULL::float8 AS drift_per_day
	`, `
		id::text, entity_id::text, label_id::text,
		NULL AS label_name, direction, entry_type, period_days,
		variable_method, projected_rate_per_day, conditions, priority, status, source,
		recurrence_anchor, next_due_date, project_tentatively,
		pending_amount_cents, pending_effective_date,
		start_date, end_date, created_at
	`),
		entityID, id,
		in.LabelID, in.Direction, in.EntryType, in.PeriodDays,
		in.VariableMethod, in.ProjectedRatePerDay, in.Conditions, in.Priority,
		in.Status, in.ProjectTentatively, in.StartDate, in.EndDate,
	)
	if err != nil {
		return EntryRow{}, err
	}
	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[EntryRow])
	if err != nil {
		return EntryRow{}, err
	}
	// Clean up the previous label if label_id changed and it is now orphaned.
	if oldLabelID != nil {
		newIsNil := in.LabelID == nil
		changed := newIsNil || *in.LabelID != *oldLabelID
		if changed {
			_ = s.DeleteLabelIfOrphaned(ctx, entityID, *oldLabelID)
		}
	}
	return result, nil
}

// DeleteEntry removes an entry row and cleans up the label if it becomes orphaned.
func (s *Store) DeleteEntry(ctx context.Context, entityID, id string) error {
	var labelID *string
	_ = s.pool.QueryRow(ctx, `
		SELECT label_id::text FROM entries WHERE entity_id = $1 AND id = $2
	`, entityID, id).Scan(&labelID)

	tag, err := s.pool.Exec(ctx, `
		DELETE FROM entries WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if labelID != nil {
		_ = s.DeleteLabelIfOrphaned(ctx, entityID, *labelID)
	}
	return nil
}

// PreviewConditions returns matching transaction ids for the given conditions JSONB.
// This is a simplified match on merchant_normalized using the "merchants" field.
func (s *Store) PreviewConditions(ctx context.Context, entityID string, conditions json.RawMessage) (int, []string, error) {
	var m map[string]interface{}
	if err := json.Unmarshal(conditions, &m); err != nil {
		return 0, nil, nil
	}

	merchants, ok := m["merchants"]
	if !ok {
		return 0, []string{}, nil
	}

	var merchantList []string
	switch v := merchants.(type) {
	case []interface{}:
		for _, item := range v {
			if s, ok := item.(string); ok {
				merchantList = append(merchantList, s)
			}
		}
	}

	if len(merchantList) == 0 {
		return 0, []string{}, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id::text FROM transactions
		WHERE entity_id = $1 AND merchant_normalized ILIKE ANY($2)
		LIMIT 200
	`, entityID, merchantList)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	return len(ids), ids, rows.Err()
}

// UpdateEntryConditions updates the conditions JSON on an entry, releases all
// transaction_entry_assignments for it, and resets its status to pending_review.
// Both writes happen in a single transaction so they are atomic.
func (s *Store) UpdateEntryConditions(ctx context.Context, entityID, id string, conditions json.RawMessage) (EntryRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EntryRow{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	tag, err := tx.Exec(ctx, `
		UPDATE entries
		SET conditions = $3, status = 'pending_review'
		WHERE entity_id = $1 AND id = $2::uuid
	`, entityID, id, conditions)
	if err != nil {
		return EntryRow{}, err
	}
	if tag.RowsAffected() == 0 {
		return EntryRow{}, pgx.ErrNoRows
	}

	if _, err := tx.Exec(ctx, `
		DELETE FROM transaction_entry_assignments WHERE entry_id = $1::uuid
	`, id); err != nil {
		return EntryRow{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return EntryRow{}, err
	}

	return s.GetEntry(ctx, entityID, id)
}

// ActivateEntry sets an entry's status to active.
func (s *Store) ActivateEntry(ctx context.Context, entityID, entryID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entries SET status = 'active' WHERE entity_id = $1 AND id = $2
	`, entityID, entryID)
	return err
}

// DeactivateEntry sets an entry's status to inactive.
func (s *Store) DeactivateEntry(ctx context.Context, entityID, entryID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entries SET status = 'inactive' WHERE entity_id = $1 AND id = $2
	`, entityID, entryID)
	return err
}

// EndEntry sets end_date on an entry.
func (s *Store) EndEntry(ctx context.Context, entityID, entryID string, endDate time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entries SET end_date = $3 WHERE entity_id = $1 AND id = $2
	`, entityID, entryID, endDate)
	return err
}

// ApproveEntryReview activates or ends the entry based on its alert_type,
// records the reviewer, and returns the alert_type for job-trigger decisions.
func (s *Store) ApproveEntryReview(ctx context.Context, entityID, entryID, userID string) (string, error) {
	var alertType string
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(alert_type, '') FROM entries
		WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending_review'
	`, entityID, entryID).Scan(&alertType)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}

	switch alertType {
	case "ended":
		if _, err := s.pool.Exec(ctx, `
			UPDATE entries SET end_date = NOW()::date, reviewed_by = $3::uuid, reviewed_at = NOW()
			WHERE entity_id = $1 AND id = $2::uuid
		`, entityID, entryID, userID); err != nil {
			return alertType, err
		}
	default: // "new", "" — activate
		if _, err := s.pool.Exec(ctx, `
			UPDATE entries SET status = 'active', reviewed_by = $3::uuid, reviewed_at = NOW()
			WHERE entity_id = $1 AND id = $2::uuid
		`, entityID, entryID, userID); err != nil {
			return alertType, err
		}
	}
	return alertType, nil
}

// RejectEntryReview deactivates the entry and records the reviewer.
func (s *Store) RejectEntryReview(ctx context.Context, entityID, entryID, userID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entries SET status = 'inactive', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending_review'
	`, entityID, entryID, userID)
	return err
}

// EntryCounts holds per-status counts for Ledger filter pills.
type EntryCounts struct {
	PendingReview int
	Active        int
	Inactive      int
}

// CountEntriesByStatus returns the number of entries per status for a given entity.
func (s *Store) CountEntriesByStatus(ctx context.Context, entityID string) (EntryCounts, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT status, COUNT(*)::int
		FROM entries
		WHERE entity_id = $1
		GROUP BY status
	`, entityID)
	if err != nil {
		return EntryCounts{}, err
	}
	defer rows.Close()
	var c EntryCounts
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return EntryCounts{}, err
		}
		switch status {
		case "pending_review":
			c.PendingReview = count
		case "active":
			c.Active = count
		case "inactive":
			c.Inactive = count
		}
	}
	return c, rows.Err()
}

// ListAllEntriesSorted returns up to 1000 entries sorted so pending_review
// comes first, then active, then inactive; within each group ordered by
// start_date DESC, id DESC. No cursor pagination — used by the Ledger "all" view.
func (s *Store) ListAllEntriesSorted(ctx context.Context, entityID string) ([]EntryRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
		WHERE e.entity_id = $1
		ORDER BY
			CASE e.status
				WHEN 'pending_review' THEN 0
				WHEN 'active'         THEN 1
				ELSE                       2
			END,
			e.start_date DESC,
			e.id DESC
		LIMIT 1000
	`, entryCols), entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
}
