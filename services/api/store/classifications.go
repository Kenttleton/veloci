package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Classification represents a row from the classifications table.
type Classification struct {
	ID         string          `db:"id"`
	EntityID   string          `db:"entity_id"`
	LabelID    string          `db:"label_id"`
	LabelName  *string         `db:"label_name"`
	Conditions json.RawMessage `db:"conditions"`
	Priority   int             `db:"priority"`
	Status     string          `db:"status"`
	Source     string          `db:"source"`
	CreatedAt  time.Time       `db:"created_at"`
}

const classificationCols = `
	c.id::text, c.entity_id::text, c.label_id::text, l.name AS label_name,
	c.conditions, c.priority, c.status, c.source, c.created_at
`

// GetClassification fetches a single classification by id for an entity.
func (s *Store) GetClassification(ctx context.Context, entityID, id string) (Classification, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM classifications c
		JOIN labels l ON l.id = c.label_id
		WHERE c.entity_id = $1 AND c.id = $2
	`, classificationCols), entityID, id)
	if err != nil {
		return Classification{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Classification])
}

// ListClassifications returns a paginated list of classifications for an entity.
func (s *Store) ListClassifications(ctx context.Context, entityID string, limit int, cursor string) ([]Classification, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s
			FROM classifications c
			JOIN labels l ON l.id = c.label_id
			WHERE c.entity_id = $1
			ORDER BY c.created_at DESC, c.id DESC
			LIMIT $2
		`, classificationCols), entityID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Classification])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s
		FROM classifications c
		JOIN labels l ON l.id = c.label_id
		WHERE c.entity_id = $1
		  AND (c.created_at, c.id::text) < ($2::timestamptz, $3)
		ORDER BY c.created_at DESC, c.id DESC
		LIMIT $4
	`, classificationCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Classification])
}

// CreateClassificationInput holds the fields needed to insert a classification.
type CreateClassificationInput struct {
	LabelID    string
	Conditions json.RawMessage
	Priority   int
	Source     string
}

// CreateClassification inserts a new classification row.
func (s *Store) CreateClassification(ctx context.Context, entityID string, in CreateClassificationInput) (Classification, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO classifications (id, entity_id, label_id, conditions, priority, status, source, created_at)
		VALUES (gen_random_uuid(), $1, $2::uuid, $3, $4, 'active', $5, NOW())
		RETURNING %s
	`, `
		id::text, entity_id::text, label_id::text, NULL AS label_name,
		conditions, priority, status, source, created_at
	`),
		entityID, in.LabelID, in.Conditions, in.Priority, in.Source,
	)
	if err != nil {
		return Classification{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Classification])
}

// UpdateClassificationInput holds the mutable fields for a classification update.
type UpdateClassificationInput struct {
	LabelID    string
	Conditions json.RawMessage
	Priority   int
	Status     string
}

// UpdateClassification updates mutable fields on a classification.
func (s *Store) UpdateClassification(ctx context.Context, entityID, id string, in UpdateClassificationInput) (Classification, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE classifications SET
			label_id = $3::uuid,
			conditions = $4,
			priority = $5,
			status = $6
		WHERE entity_id = $1 AND id = $2
		RETURNING %s
	`, `
		id::text, entity_id::text, label_id::text, NULL AS label_name,
		conditions, priority, status, source, created_at
	`),
		entityID, id,
		in.LabelID, in.Conditions, in.Priority, in.Status,
	)
	if err != nil {
		return Classification{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Classification])
}

// DeleteClassification removes a classification row.
func (s *Store) DeleteClassification(ctx context.Context, entityID, id string) error {
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM classifications WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
