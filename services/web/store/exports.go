package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Export represents a completed or in-progress export artifact.
type Export struct {
	ID          string          `db:"id"`
	EntityID    string          `db:"entity_id"`
	JobID       string          `db:"job_id"`
	CreatedBy   *string         `db:"created_by"`
	ExportType  string          `db:"export_type"`
	Format      string          `db:"format"`
	Parameters  json.RawMessage `db:"parameters"`
	StorageType string          `db:"storage_type"`
	StorageRef  *string         `db:"storage_ref"`
	Data        []byte          `db:"data"`
	SizeBytes   *int64          `db:"size_bytes"`
	Filename    string          `db:"filename"`
	CreatedAt   time.Time       `db:"created_at"`
	ExpiresAt   time.Time       `db:"expires_at"`
}

const exportCols = `
	id::text, entity_id::text, job_id::text, created_by::text,
	export_type, format, parameters, storage_type, storage_ref,
	data, size_bytes, filename, created_at, expires_at
`

// CreateExport inserts an export artifact row.
func (s *Store) CreateExport(
	ctx context.Context,
	entityID, jobID, createdBy, exportType, format string,
	parameters json.RawMessage,
	storageType string, storageRef *string,
	data []byte, sizeBytes int64,
	filename string,
) (Export, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO exports (
			entity_id, job_id, created_by, export_type, format, parameters,
			storage_type, storage_ref, data, size_bytes, filename
		) VALUES ($1, $2, $3::uuid, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING %s
	`, exportCols),
		entityID, jobID, createdBy, exportType, format, parameters,
		storageType, storageRef, data, sizeBytes, filename,
	)
	if err != nil {
		return Export{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Export])
}

// GetExportByJob fetches the export artifact for a given job, scoped to the entity.
func (s *Store) GetExportByJob(ctx context.Context, entityID, jobID string) (Export, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM exports
		WHERE entity_id = $1 AND job_id = $2
	`, exportCols), entityID, jobID)
	if err != nil {
		return Export{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Export])
}

// ListExports returns paginated exports for an entity, newest first.
func (s *Store) ListExports(ctx context.Context, entityID string, limit int, cursor string) ([]Export, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM exports
			WHERE entity_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, exportCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Export])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM exports
		WHERE entity_id = $1
		  AND (created_at, id::text) < ($2::timestamptz, $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, exportCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Export])
}

// DeleteExpiredExports removes exports past their expiry. Returns the count deleted.
func (s *Store) DeleteExpiredExports(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM exports WHERE expires_at < (NOW() AT TIME ZONE 'UTC')
	`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
