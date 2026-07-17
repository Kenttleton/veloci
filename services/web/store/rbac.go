package store

import (
	"context"

	"github.com/veloci/veloci/middleware"
)

// LoadPermissions reads all role→permission pairs and returns a PermissionCache.
func (s *Store) LoadPermissions(ctx context.Context) (middleware.PermissionCache, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.name AS role, p.name AS perm
		FROM roles r
		JOIN role_permissions rp ON rp.role_id = r.id
		JOIN permissions p ON p.id = rp.permission_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cache := make(middleware.PermissionCache)
	for rows.Next() {
		var role, perm string
		if err := rows.Scan(&role, &perm); err != nil {
			return nil, err
		}
		if cache[role] == nil {
			cache[role] = make(map[string]struct{})
		}
		cache[role][perm] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cache, nil
}
