package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Label represents a row from the labels table. Labels are global (no entity_id).
type Label struct {
	ID        string    `db:"id"`
	Name      string    `db:"name"`
	CreatedAt time.Time `db:"created_at"`
}

const labelCols = `id::text, name, created_at`

// GetLabel fetches a single label by id.
func (s *Store) GetLabel(ctx context.Context, id string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM labels WHERE id = $1
	`, labelCols), id)
	if err != nil {
		return Label{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
}

// ListLabels returns a paginated list of all labels.
func (s *Store) ListLabels(ctx context.Context, limit int, cursor string) ([]Label, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM labels
			ORDER BY created_at DESC, id DESC
			LIMIT $1
		`, labelCols), limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[Label])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM labels
		WHERE (created_at, id::text) < ($1::timestamptz, $2)
		ORDER BY created_at DESC, id DESC
		LIMIT $3
	`, labelCols), cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Label])
}

// CreateLabel inserts a new label. Returns pgx.ErrNoRows wrapped if name conflicts.
func (s *Store) CreateLabel(ctx context.Context, name string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO labels (id, name, created_at)
		VALUES (gen_random_uuid(), $1, NOW())
		RETURNING %s
	`, labelCols), name)
	if err != nil {
		return Label{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
}

// UpdateLabel renames a label.
func (s *Store) UpdateLabel(ctx context.Context, id, name string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE labels SET name = $2 WHERE id = $1
		RETURNING %s
	`, labelCols), id, name)
	if err != nil {
		return Label{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
}

// DeleteLabel removes a label row.
func (s *Store) DeleteLabel(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM labels WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// ListEntriesByLabel returns active entries associated with a label.
func (s *Store) ListEntriesByLabel(ctx context.Context, entityID, labelID string, limit int, cursor string) ([]EntryRow, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM entries e
			LEFT JOIN labels l ON l.id = e.label_id
			WHERE e.entity_id = $1 AND e.label_id = $2
			ORDER BY e.created_at DESC, e.id DESC
			LIMIT $3
		`, entryCols), entityID, labelID, limit)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
	}

	cursorID, cursorTS, err := decodeCursor(cursor)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM entries e
		LEFT JOIN labels l ON l.id = e.label_id
		WHERE e.entity_id = $1 AND e.label_id = $2
		  AND (e.created_at, e.id::text) < ($3::timestamptz, $4)
		ORDER BY e.created_at DESC, e.id DESC
		LIMIT $5
	`, entryCols), entityID, labelID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[EntryRow])
}
