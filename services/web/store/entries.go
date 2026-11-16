package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrSystemEntry = errors.New("system entry cannot be modified")

// EntryRow is the full entries row joined with optional label name and latest snapshot.
type EntryRow struct {
	ID                  string          `db:"id"`
	EntityID            string          `db:"entity_id"`
	LabelID             *string         `db:"label_id"`
	LabelName           *string         `db:"label_name"`
	Direction           string          `db:"direction"`
	EntryType           string          `db:"entry_type"`
	PeriodDays          *int            `db:"period_days"`
	RateMethod          *string         `db:"rate_method"`
	ProjectedRatePerDay *float64        `db:"projected_rate_per_day"`
	Conditions          json.RawMessage `db:"conditions"`
	Priority            int             `db:"priority"`
	Status              string          `db:"status"`
	Source              string          `db:"source"`
	RecurrenceAnchor    *string         `db:"recurrence_anchor"`
	NextDueDate         *time.Time      `db:"next_due_date"`
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
	e.direction, e.entry_type, e.period_days, e.rate_method,
	e.projected_rate_per_day, e.conditions, e.priority, e.status, e.source,
	e.recurrence_anchor, e.next_due_date,
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
// excludeJobIDs skips snapshot rows written by in-progress engine jobs so the
// returned rates stay consistent with the previous completed run during a crawl.
func (s *Store) ListEntries(ctx context.Context, entityID string, dr DateRange, accountID, statusFilter string, limit int, cursor string, excludeJobIDs []string) ([]EntryRow, error) {
	statusCond := `e.status = 'live'`
	switch statusFilter {
	case "all":
		statusCond = `e.source != 'system'`
	case "pending", "ended":
		statusCond = `e.status = '` + statusFilter + `' AND e.source != 'system'`
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

	snapshotExclude := ""
	if len(excludeJobIDs) > 0 {
		args = append(args, excludeJobIDs)
		snapshotExclude = fmt.Sprintf("\n\t\t\tAND NOT (job_id::text = ANY($%d))", len(args))
	}

	entryFrom := fmt.Sprintf(`
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'%s
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
	`, snapshotExclude)

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

// GetSystemEntryID returns the UUID of the system entry for the given direction.
// Returns an empty string when no system entry exists yet.
func (s *Store) GetSystemEntryID(ctx context.Context, entityID, direction string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		SELECT id::text FROM entries
		WHERE entity_id = $1 AND source = 'system' AND direction = $2
		LIMIT 1
	`, entityID, direction).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ListEntriesForPeriod returns live non-mixed entries with their most-recent snapshot
// on or before periodEnd. Used by the BudgetStack fragment to show historical data.
// excludeJobIDs skips in-progress engine job rows; pass nil for historical dates
// where the running job's rows are irrelevant.
func (s *Store) ListEntriesForPeriod(ctx context.Context, entityID string, periodEnd time.Time, excludeJobIDs []string) ([]EntryRow, error) {
	snapshotExclude := ""
	extraArgs := []any{entityID, periodEnd}
	if len(excludeJobIDs) > 0 {
		extraArgs = append(extraArgs, excludeJobIDs)
		snapshotExclude = fmt.Sprintf("\n\t\t\t  AND NOT (job_id::text = ANY($%d))", len(extraArgs))
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		LEFT JOIN LATERAL (
			SELECT actual_rate_per_day, drift_per_day
			FROM snapshots
			WHERE entity_id = e.entity_id AND node_id = e.id AND node_type = 'entry'
			  AND snapshot_date <= $2::date%s
			ORDER BY snapshot_date DESC LIMIT 1
		) s ON true
		WHERE e.entity_id = $1 AND e.status = 'live' AND e.direction != 'mixed'
		ORDER BY e.start_date DESC, e.id DESC
	`, entryCols, snapshotExclude), extraArgs...)
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
	RateMethod           *string
	ProjectedRatePerDay  *float64
	Conditions           json.RawMessage
	Priority             int
	Source               string
	StartDate            time.Time
	EndDate              *time.Time
}

// CreateEntry inserts a new entry row with status='pending'.
func (s *Store) CreateEntry(ctx context.Context, entityID string, in CreateEntryInput) (EntryRow, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO entries (
			id, entity_id, label_id, direction, entry_type, period_days,
			rate_method, projected_rate_per_day, conditions, priority,
			status, source, start_date, end_date, created_at
		) VALUES (
			gen_random_uuid(), $1, $2::uuid, $3, $4, $5,
			$6, $7, $8, $9,
			'pending', $10, $11, $12, NOW()
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
		rate_method, projected_rate_per_day, conditions, priority, status, source,
		recurrence_anchor, next_due_date,
		pending_amount_cents, pending_effective_date,
		start_date, end_date, created_at
	`),
		entityID, in.LabelID, in.Direction, in.EntryType, in.PeriodDays,
		in.RateMethod, in.ProjectedRatePerDay, in.Conditions, in.Priority,
		in.Source, in.StartDate, in.EndDate,
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
	RateMethod          *string
	ProjectedRatePerDay *float64
	Conditions          json.RawMessage
	Priority            int
	Status              string
	StartDate           time.Time
	EndDate             *time.Time
	RecurrenceAnchor    *string
	NextDueDate         *time.Time
}

// UpdateEntry updates mutable fields on an entry. When label_id changes, the
// previous label is cleaned up if it has no remaining entry associations.
func (s *Store) UpdateEntry(ctx context.Context, entityID, id string, in UpdateEntryInput) (EntryRow, error) {
	var oldLabelID *string
	var source string
	_ = s.pool.QueryRow(ctx, `
		SELECT label_id::text, source FROM entries WHERE entity_id = $1 AND id = $2
	`, entityID, id).Scan(&oldLabelID, &source)
	if source == "system" {
		return EntryRow{}, ErrSystemEntry
	}

	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE entries SET
			label_id = $3::uuid,
			direction = $4,
			entry_type = $5,
			period_days = $6,
			rate_method = $7,
			projected_rate_per_day = $8,
			conditions = $9,
			priority = $10,
			status = $11,
			start_date = $12,
			end_date = $13,
			recurrence_anchor = $14,
			next_due_date = $15
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
		rate_method, projected_rate_per_day, conditions, priority, status, source,
		recurrence_anchor, next_due_date,
		pending_amount_cents, pending_effective_date,
		start_date, end_date, created_at
	`),
		entityID, id,
		in.LabelID, in.Direction, in.EntryType, in.PeriodDays,
		in.RateMethod, in.ProjectedRatePerDay, in.Conditions, in.Priority,
		in.Status, in.StartDate, in.EndDate,
		in.RecurrenceAnchor, in.NextDueDate,
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
	var source string
	_ = s.pool.QueryRow(ctx, `
		SELECT label_id::text, source FROM entries WHERE entity_id = $1 AND id = $2
	`, entityID, id).Scan(&labelID, &source)
	if source == "system" {
		return ErrSystemEntry
	}

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

// TransactionPreview is a lightweight transaction row for condition preview.
type TransactionPreview struct {
	ID                 string `json:"id"`
	Date               string `json:"date"`
	AmountCents        int64  `json:"amount_cents"`
	MerchantNormalized string `json:"merchant_normalized"`
	ImportedPayee      string `json:"imported_payee"`
	SettlementStatus   string `json:"settlement_status"`
}

// PreviewConditions evaluates Schema B conditions against transactions without
// persisting any assignments. Mirrors the engine's matching logic — returns up
// to 50 rows ordered by date DESC. Entry-target conditions (label_matched,
// entry_direction, etc.) are skipped as they require full engine context.
func (s *Store) PreviewConditions(ctx context.Context, entityID string, conditions json.RawMessage) ([]TransactionPreview, error) {
	if len(conditions) == 0 {
		return nil, nil
	}
	var root map[string]interface{}
	if err := json.Unmarshal(conditions, &root); err != nil {
		return nil, nil
	}

	ev := &condEval{args: []interface{}{entityID}}
	clause := ev.buildNode(root)
	if clause == "" {
		clause = "TRUE"
	}

	query := fmt.Sprintf(`
		SELECT t.id::text, t.date, t.amount_cents, t.merchant_normalized,
		       t.imported_payee, t.settlement_status
		FROM transactions t
		LEFT JOIN accounts a ON a.id = t.account_id
		WHERE t.entity_id = $1 AND (%s)
		ORDER BY t.date DESC
		LIMIT 50
	`, clause)

	rows, err := s.pool.Query(ctx, query, ev.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []TransactionPreview
	for rows.Next() {
		var p TransactionPreview
		var date time.Time
		if err := rows.Scan(&p.ID, &date, &p.AmountCents, &p.MerchantNormalized, &p.ImportedPayee, &p.SettlementStatus); err != nil {
			return nil, err
		}
		p.Date = date.Format("2006-01-02")
		result = append(result, p)
	}
	return result, rows.Err()
}

// condEval builds a parameterized SQL WHERE clause from Schema B conditions JSON.
// $1 is always entityID; additional params are appended via p().
type condEval struct {
	args []interface{}
}

func (e *condEval) p(v interface{}) string {
	e.args = append(e.args, v)
	return fmt.Sprintf("$%d", len(e.args))
}

// buildNode converts a conditions object (all keys AND'd) to a SQL fragment.
func (e *condEval) buildNode(node map[string]interface{}) string {
	var clauses []string
	for k, v := range node {
		if c := e.buildLeaf(k, v); c != "" {
			clauses = append(clauses, c)
		}
	}
	switch len(clauses) {
	case 0:
		return ""
	case 1:
		return clauses[0]
	default:
		return "(" + strings.Join(clauses, " AND ") + ")"
	}
}

func (e *condEval) buildLeaf(key string, val interface{}) string {
	switch key {
	case "payee_contains":
		s, _ := val.(string)
		return "t.merchant_normalized ILIKE " + e.p("%"+s+"%")
	case "payee_exact":
		s, _ := val.(string)
		return "t.merchant_normalized ILIKE " + e.p(s)
	case "payee_starts_with":
		s, _ := val.(string)
		return "t.merchant_normalized ILIKE " + e.p(s+"%")
	case "payee_ends_with":
		s, _ := val.(string)
		return "t.merchant_normalized ILIKE " + e.p("%"+s)
	case "payee_not_contains":
		s, _ := val.(string)
		return "t.merchant_normalized NOT ILIKE " + e.p("%"+s+"%")
	case "payee_regex":
		s, _ := val.(string)
		return "t.merchant_normalized ~* " + e.p(s)
	case "payee_one_of":
		arr, _ := val.([]interface{})
		var strs []string
		for _, item := range arr {
			if s, ok := item.(string); ok {
				strs = append(strs, s)
			}
		}
		if len(strs) == 0 {
			return ""
		}
		return "t.merchant_normalized ILIKE ANY(" + e.p(strs) + ")"
	case "amount_range":
		obj, _ := val.(map[string]interface{})
		var parts []string
		if min, ok := obj["min"].(float64); ok {
			parts = append(parts, "t.amount_cents >= "+e.p(int64(math.Round(min*100))))
		}
		if max, ok := obj["max"].(float64); ok {
			parts = append(parts, "t.amount_cents <= "+e.p(int64(math.Round(max*100))))
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " AND ") + ")"
	case "date_range":
		obj, _ := val.(map[string]interface{})
		var parts []string
		if start, ok := obj["start"].(string); ok {
			parts = append(parts, "t.date >= "+e.p(start)+"::date")
		}
		if end, ok := obj["end"].(string); ok {
			parts = append(parts, "t.date <= "+e.p(end)+"::date")
		}
		if len(parts) == 0 {
			return ""
		}
		return "(" + strings.Join(parts, " AND ") + ")"
	case "date_day_of_month":
		obj, _ := val.(map[string]interface{})
		day, ok := obj["day"].(float64)
		if !ok {
			return ""
		}
		tol := 0.0
		if t, ok := obj["tolerance_days"].(float64); ok {
			tol = t
		}
		if tol == 0 {
			return "EXTRACT(DAY FROM t.date) = " + e.p(int(day))
		}
		return "(EXTRACT(DAY FROM t.date) BETWEEN " + e.p(int(day-tol)) + " AND " + e.p(int(day+tol)) + ")"
	case "account":
		s, _ := val.(string)
		if s == "" {
			return ""
		}
		return "t.account_id IN (SELECT id FROM accounts WHERE entity_id = $1 AND LOWER(name) = LOWER(" + e.p(s) + "))"
	case "institution":
		s, _ := val.(string)
		if s == "" {
			return ""
		}
		return "a.institution_id IN (SELECT id FROM institutions WHERE entity_id = $1 AND LOWER(institution_name) = LOWER(" + e.p(s) + "))"
	case "and":
		arr, _ := val.([]interface{})
		return e.buildLogic(arr, "AND")
	case "or":
		arr, _ := val.([]interface{})
		return e.buildLogic(arr, "OR")
	case "not":
		child, ok := val.(map[string]interface{})
		if !ok {
			return ""
		}
		c := e.buildNode(child)
		if c == "" {
			return ""
		}
		return "NOT (" + c + ")"
	default:
		return "" // entry-target or unknown — skip
	}
}

func (e *condEval) buildLogic(arr []interface{}, op string) string {
	var parts []string
	for _, item := range arr {
		child, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		if c := e.buildNode(child); c != "" {
			parts = append(parts, c)
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	default:
		return "(" + strings.Join(parts, " "+op+" ") + ")"
	}
}

// UpdateEntryConditions updates the conditions JSON on an entry, releases all
// transaction_entry_assignments for it, and resets its status to pending.
// Both writes happen in a single transaction so they are atomic.
func (s *Store) UpdateEntryConditions(ctx context.Context, entityID, id string, conditions json.RawMessage) (EntryRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EntryRow{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var entrySource string
	if err := tx.QueryRow(ctx, `SELECT source FROM entries WHERE entity_id = $1 AND id = $2::uuid`, entityID, id).Scan(&entrySource); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return EntryRow{}, pgx.ErrNoRows
		}
		return EntryRow{}, err
	}
	if entrySource == "system" {
		return EntryRow{}, ErrSystemEntry
	}

	tag, err := tx.Exec(ctx, `
		UPDATE entries
		SET conditions = $3, status = 'pending'
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
		UPDATE entries SET status = 'live' WHERE entity_id = $1 AND id = $2
	`, entityID, entryID)
	return err
}

// DeactivateEntry sets an entry's status to inactive.
func (s *Store) DeactivateEntry(ctx context.Context, entityID, entryID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE entries SET status = 'ended' WHERE entity_id = $1 AND id = $2
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
		WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending'
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
			UPDATE entries SET status = 'live', reviewed_by = $3::uuid, reviewed_at = NOW()
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
		UPDATE entries SET status = 'ended', reviewed_by = $3::uuid, reviewed_at = NOW()
		WHERE entity_id = $1 AND id = $2::uuid AND status = 'pending'
	`, entityID, entryID, userID)
	return err
}

// EntryCounts holds per-status counts for Ledger filter pills.
type EntryCounts struct {
	Pending int
	Live    int
	Ended   int
	System  int
}

// CountEntriesByStatus returns the number of entries per status for a given entity.
// System entries are counted separately so the All pill can include them while
// Review/Live/Ended pill counts reflect workflow entries only.
func (s *Store) CountEntriesByStatus(ctx context.Context, entityID string) (EntryCounts, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT status, COUNT(*)::int
		FROM entries
		WHERE entity_id = $1 AND source != 'system'
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
		case "pending":
			c.Pending = count
		case "live":
			c.Live = count
		case "ended":
			c.Ended = count
		}
	}
	if err := rows.Err(); err != nil {
		return EntryCounts{}, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*)::int FROM entries WHERE entity_id = $1 AND source = 'system'
	`, entityID).Scan(&c.System); err != nil {
		return EntryCounts{}, err
	}
	return c, nil
}

// ListAllEntriesSorted returns up to 1000 entries sorted so pending
// comes first, then live, then ended; within each group ordered by
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
				WHEN 'pending' THEN 0
				WHEN 'live'    THEN 1
				ELSE                2
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

// systemEntryOrder defines the display rank for system entries by label name.
// Unmapped entries sort after all mapped ones, preserving DB retrieval order.
var systemEntryOrder = map[string]int{
	"All":    0,
	"Income": 1,
	"Spend":  2,
}

func sortSystemEntries(entries []EntryRow) {
	rank := func(e EntryRow) int {
		if e.LabelName != nil {
			if r, ok := systemEntryOrder[*e.LabelName]; ok {
				return r
			}
		}
		return len(systemEntryOrder) + 1
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return rank(entries[i]) < rank(entries[j])
	})
}

// ListSystemEntries returns all entries with source='system' for the entity,
// sorted by the hardcoded systemEntryOrder map (All → Income → Spend).
// Unmapped future system entries appear after the known ones in retrieval order.
func (s *Store) ListSystemEntries(ctx context.Context, entityID string) ([]EntryRow, error) {
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
		WHERE e.entity_id = $1 AND e.source = 'system'
	`, entryCols), entityID)
	if err != nil {
		return nil, err
	}
	entries, err := pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
	if err != nil {
		return nil, err
	}
	sortSystemEntries(entries)
	return entries, nil
}
