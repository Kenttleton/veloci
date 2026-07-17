package middleware

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
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

// RequirePermission returns a Huma middleware that checks the cache.
// Reads entity_role from context (set by Authenticate).
// Returns 403 if permission missing.
func RequirePermission(cache PermissionCache, permission string) func(huma.Context, func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		role := EntityRole(ctx.Context())
		if !cache.Has(role, permission) {
			ctx.SetHeader("Content-Type", "application/problem+json")
			ctx.SetStatus(http.StatusForbidden)
			ctx.BodyWriter().Write([]byte(`{"status":403,"title":"Forbidden","detail":"forbidden"}`)) //nolint:errcheck
			return
		}
		next(ctx)
	}
}
