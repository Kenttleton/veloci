package store

import (
	"context"
)

// EntitySummary is a minimal admin view of an entity.
type EntitySummary struct {
	EntityID  string `db:"entity_id"`
	UserCount int    `db:"user_count"`
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
