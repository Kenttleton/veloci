package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// PermissionCache maps entity_role to a set of permission names.
// Loaded at startup from DB and never reloaded per-request.
type PermissionCache map[string]map[string]struct{}

// Has returns true when the given entity role has the named permission.
func (c PermissionCache) Has(entityRole, permission string) bool {
	perms, ok := c[entityRole]
	if !ok {
		return false
	}
	_, found := perms[permission]
	return found
}

// RequirePermission returns an Echo middleware that checks the cache.
// Reads entity_role from context (set by Authenticate).
// Returns 403 if permission missing.
func RequirePermission(cache PermissionCache, permission string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role := EntityRole(c.Request().Context())
			if !cache.Has(role, permission) {
				return echo.NewHTTPError(http.StatusForbidden, "forbidden")
			}
			return next(c)
		}
	}
}
