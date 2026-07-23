package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var ErrLabelInUse  = errors.New("label is in use by entries")
var ErrSystemLabel = errors.New("system label cannot be modified")

// Label represents a row from the labels table.
type Label struct {
	ID        string    `db:"id"`
	EntityID  string    `db:"entity_id"`
	Name      string    `db:"name"`
	Scope     *string   `db:"scope"`
	CreatedAt time.Time `db:"created_at"`
}

const labelCols = `id::text, entity_id::text, name, scope, created_at`

// GetLabel fetches a single label by id scoped to the entity.
func (s *Store) GetLabel(ctx context.Context, entityID, id string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT %s FROM labels WHERE entity_id = $1 AND id = $2
	`, labelCols), entityID, id)
	if err != nil {
		return Label{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
}

// ListLabels returns a paginated list of labels for the entity.
func (s *Store) ListLabels(ctx context.Context, entityID string, limit int, cursor string) ([]Label, error) {
	if cursor == "" {
		rows, err := s.pool.Query(ctx, fmt.Sprintf(`
			SELECT %s FROM labels
			WHERE entity_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`, labelCols), entityID, limit)
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
		WHERE entity_id = $1
		  AND (created_at, id::text) < ($2::timestamptz, $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`, labelCols), entityID, cursorTS, cursorID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[Label])
}

// CreateLabel inserts a new label for the entity.
func (s *Store) CreateLabel(ctx context.Context, entityID, name string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		INSERT INTO labels (id, entity_id, name, created_at)
		VALUES (gen_random_uuid(), $1, $2, NOW())
		RETURNING %s
	`, labelCols), entityID, name)
	if err != nil {
		return Label{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
}

// UpdateLabel renames a label. Returns ErrSystemLabel for system-scoped labels.
func (s *Store) UpdateLabel(ctx context.Context, entityID, id, name string) (Label, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		UPDATE labels SET name = $3
		WHERE entity_id = $1 AND id = $2 AND scope IS DISTINCT FROM 'system'
		RETURNING %s
	`, labelCols), entityID, id, name)
	if err != nil {
		return Label{}, err
	}
	label, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[Label])
	if errors.Is(err, pgx.ErrNoRows) {
		if exists, _ := s.labelExists(ctx, entityID, id); exists {
			return Label{}, ErrSystemLabel
		}
	}
	return label, err
}

// DeleteLabel removes a label. Returns ErrSystemLabel for system-scoped labels,
// ErrLabelInUse if any entries still reference it.
func (s *Store) DeleteLabel(ctx context.Context, entityID, id string) error {
	label, err := s.GetLabel(ctx, entityID, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return pgx.ErrNoRows
	}
	if err != nil {
		return err
	}
	if label.Scope != nil && *label.Scope == "system" {
		return ErrSystemLabel
	}

	var count int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM entries WHERE entity_id = $1 AND label_id = $2::uuid
	`, entityID, id).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return ErrLabelInUse
	}
	tag, err := s.pool.Exec(ctx, `
		DELETE FROM labels WHERE entity_id = $1 AND id = $2
	`, entityID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// DeleteLabelIfOrphaned deletes the label only when no entries reference it.
// Skips system-scoped labels. Safe to call speculatively.
func (s *Store) DeleteLabelIfOrphaned(ctx context.Context, entityID, labelID string) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM labels
		WHERE entity_id = $1 AND id = $2::uuid
		  AND scope IS NULL
		  AND NOT EXISTS (
			SELECT 1 FROM entries WHERE entity_id = $1 AND label_id = $2::uuid
		  )
	`, entityID, labelID)
	return err
}

// EnsureSystemLabels creates the Income and Spend system labels for the entity
// if they do not already exist. Safe to call on every startup.
func (s *Store) EnsureSystemLabels(ctx context.Context, entityID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO labels (id, entity_id, name, scope, created_at)
		VALUES
			(gen_random_uuid(), $1::uuid, 'Income', 'system', NOW()),
			(gen_random_uuid(), $1::uuid, 'Spend',  'system', NOW())
		ON CONFLICT (entity_id, name) DO UPDATE SET scope = 'system'
	`, entityID)
	return err
}

func (s *Store) labelExists(ctx context.Context, entityID, id string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM labels WHERE entity_id = $1 AND id = $2)
	`, entityID, id).Scan(&exists)
	return exists, err
}

// LabelWithCount extends Label with the entry count for the entity.
type LabelWithCount struct {
	Label
	EntryCount int `db:"entry_count"`
}

// ListLabelsWithEntryCount returns all entity labels ordered by creation date,
// with the count of entries that reference each label.
func (s *Store) ListLabelsWithEntryCount(ctx context.Context, entityID string) ([]LabelWithCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.id::text, l.entity_id::text, l.name, l.scope, l.created_at,
		       COUNT(e.id)::int AS entry_count
		FROM labels l
		LEFT JOIN entries e ON e.label_id = l.id AND e.entity_id = l.entity_id
		WHERE l.entity_id = $1 AND l.scope IS DISTINCT FROM 'system'
		GROUP BY l.id, l.entity_id, l.name, l.scope, l.created_at
		ORDER BY l.created_at DESC, l.id DESC
	`, entityID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[LabelWithCount])
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
