package store

import (
	"context"
)

// EntitySummary is a minimal admin view of an entity.
type EntitySummary struct {
	EntityID  string `db:"entity_id"`
	UserCount int    `db:"user_count"`
}

// EnsureAdminEntity returns the ID of the first entity, creating one with the given
// name if none exists. Used during first-instance bootstrap.
func (s *Store) EnsureAdminEntity(ctx context.Context, name string) (string, error) {
	var id string
	// Return the first existing entity to avoid creating duplicates on restart.
	err := s.pool.QueryRow(ctx, `SELECT id::text FROM entities ORDER BY created_at LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO entities (name) VALUES ($1) RETURNING id::text
	`, name).Scan(&id)
	return id, err
}

// EnsureEntityUser adds a user to an entity with the given role if not already present.
func (s *Store) EnsureEntityUser(ctx context.Context, userID, entityID, role string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entity_users (user_id, entity_id, entity_role)
		VALUES ($1::uuid, $2::uuid, $3)
		ON CONFLICT (user_id, entity_id) DO NOTHING
	`, userID, entityID, role)
	return err
}

// ListEntities returns a summary of all entities for server admin use.
func (s *Store) ListEntities(ctx context.Context) ([]EntitySummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT entity_id::text, COUNT(*)::int AS user_count
		FROM entity_users
		GROUP BY entity_id
		ORDER BY entity_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []EntitySummary
	for rows.Next() {
		var e EntitySummary
		if err := rows.Scan(&e.EntityID, &e.UserCount); err != nil {
			return nil, err
		}
		result = append(result, e)
	}
	return result, rows.Err()
}
