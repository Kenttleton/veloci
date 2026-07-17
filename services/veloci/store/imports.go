package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PendingImport represents a row from the pending_imports table.
type PendingImport struct {
	ID             string    `db:"id"`
	EntityID       string    `db:"entity_id"`
	AccountID      string    `db:"account_id"`
	InstitutionID  *string   `db:"institution_id"`
	UploadedBy     string    `db:"uploaded_by"`
	UploadedAt     time.Time `db:"uploaded_at"`
	DateRangeStart *time.Time `db:"date_range_start"`
	DateRangeEnd   *time.Time `db:"date_range_end"`
	RowCount       *int      `db:"row_count"`
	Status         string    `db:"status"`
	JobID          *string   `db:"job_id"`
	Error          *string   `db:"error"`
}

const pendingImportCols = `
	id::text, entity_id::text, account_id::text, institution_id::text,
	uploaded_by::text, uploaded_at, date_range_start, date_range_end,
	row_count, status, job_id::text, error
`

// GetPendingImport fetches a single pending_import by id for an entity.
func (s *Store) GetPendingImport(ctx context.Context, entityID, id string) (PendingImport, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM pending_imports
		WHERE entity_id = $1 AND id = $2
	`, pendingImportCols), entityID, id)
	if err != nil {
		return PendingImport{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[PendingImport])
}

// ListPendingImports returns a paginated list of imports for an entity, optionally
// filtered to a single account (empty accountID means no filter).
func (s *Store) ListPendingImports(ctx context.Context, entityID, accountID string, limit int, cursor string) ([]PendingImport, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM pending_imports
			WHERE entity_id = $1
			  AND ($3 = '' OR account_id = $3::uuid)
			ORDER BY uploaded_at DESC, id DESC
			LIMIT $2
		`, pendingImportCols), entityID, limit, accountID)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[PendingImport])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM pending_imports
		WHERE entity_id = $1
		  AND (uploaded_at, id::text) < ($2::timestamptz, $3)
		  AND ($5 = '' OR account_id = $5::uuid)
		ORDER BY uploaded_at DESC, id DESC
		LIMIT $4
	`, pendingImportCols), entityID, cursorTS, cursorID, limit, accountID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[PendingImport])
}

// CreatePendingImport inserts a new pending_imports row and returns its id.
func (s *Store) CreatePendingImport(
	ctx context.Context,
	entityID, accountID, uploadedBy string,
	institutionID *string,
	dateRangeStart, dateRangeEnd time.Time,
	rowCount int,
	csvBytes []byte,
) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO pending_imports (
			id, entity_id, account_id, institution_id, uploaded_by,
			uploaded_at, date_range_start, date_range_end, row_count,
			csv_bytes, status
		) VALUES (
			gen_random_uuid(), $1, $2::uuid, $3::uuid, $4::uuid,
			NOW(), $5::date, $6::date, $7,
			$8, 'pending'
		)
		RETURNING id::text
	`, entityID, accountID, institutionID, uploadedBy,
		dateRangeStart.Format("2006-01-02"), dateRangeEnd.Format("2006-01-02"), rowCount,
		csvBytes).Scan(&id)
	return id, err
}

// SetPendingImportJob links a processing_jobs row to a pending_import.
func (s *Store) SetPendingImportJob(ctx context.Context, importID, jobID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE pending_imports SET job_id = $2::uuid WHERE id = $1
	`, importID, jobID)
	return err
}
