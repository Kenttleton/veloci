package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProcessingJob represents a row from the processing_jobs table.
type ProcessingJob struct {
	ID          string          `db:"id"`
	EntityID    string          `db:"entity_id"`
	JobType     string          `db:"job_type"`
	TriggeredBy string          `db:"triggered_by"`
	Status      string          `db:"status"`
	QueuedAt    time.Time       `db:"queued_at"`
	StartedAt   *time.Time      `db:"started_at"`
	CompletedAt *time.Time      `db:"completed_at"`
	Error       *string         `db:"error"`
	Metadata    json.RawMessage `db:"metadata"`
}

const jobCols = `
	id::text, entity_id::text, job_type, triggered_by::text,
	status, queued_at, started_at, completed_at, error, metadata
`

// GetJob fetches a single processing_jobs row by id for an entity.
func (s *Store) GetJob(ctx context.Context, entityID, id string) (ProcessingJob, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM processing_jobs
		WHERE entity_id = $1 AND id = $2
	`, jobCols), entityID, id)
	if err != nil {
		return ProcessingJob{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[ProcessingJob])
}

// ListJobs returns a paginated list of processing_jobs for an entity.
func (s *Store) ListJobs(ctx context.Context, entityID string, limit int, cursor string) ([]ProcessingJob, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM processing_jobs
			WHERE entity_id = $1
			ORDER BY queued_at DESC, id DESC
			LIMIT $2
		`, jobCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[ProcessingJob])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM processing_jobs
		WHERE entity_id = $1
		  AND (queued_at, id::text) < ($2::timestamptz, $3)
		ORDER BY queued_at DESC, id DESC
		LIMIT $4
	`, jobCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProcessingJob])
}

// CreateJob inserts a new processing_jobs row. Returns pgx.ErrNoRows if the unique
// partial index fires (one active job per entity+type).
func (s *Store) CreateJob(ctx context.Context, entityID, jobType, triggeredBy string, metadata json.RawMessage) (ProcessingJob, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO processing_jobs (id, entity_id, job_type, triggered_by, status, queued_at, metadata)
		VALUES (gen_random_uuid(), $1, $2, $3::uuid, 'queued', NOW(), $4)
		RETURNING %s
	`, jobCols), entityID, jobType, triggeredBy, metadata)
	if err != nil {
		return ProcessingJob{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[ProcessingJob])
}

// ListActiveJobs returns all queued or processing jobs for an entity (for SSE initial state).
func (s *Store) ListActiveJobs(ctx context.Context, entityID string) ([]ProcessingJob, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM processing_jobs
		WHERE entity_id = $1 AND status IN ('queued', 'processing')
		ORDER BY queued_at ASC
	`, jobCols), entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[ProcessingJob])
}
