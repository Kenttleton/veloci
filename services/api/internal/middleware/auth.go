// Package middleware provides HTTP middleware for the veloci-api service.
package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/veloci/api/internal/authclient"
)

type contextKey string

const (
	ctxEntityID   contextKey = "entity_id"
	ctxEntityRole contextKey = "entity_role"
	ctxSystemRole contextKey = "system_role"
	ctxUserID     contextKey = "sub"
)

// Authenticate calls veloci-auth /tokens/validate on every request.
// It injects entity_id, entity_role, system_role, and sub into the request context.
func Authenticate(client *authclient.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			result, err := client.ValidateToken(r.Context(), strings.TrimPrefix(header, "Bearer "))
			if err != nil {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			var claims map[string]any
			json.Unmarshal(result.Claims, &claims)
			ctx := r.Context()
			for key, ctxK := range map[string]contextKey{
				"entity_id": ctxEntityID, "entity_role": ctxEntityRole,
				"system_role": ctxSystemRole, "sub": ctxUserID,
			} {
				if v, ok := claims[key].(string); ok {
					ctx = context.WithValue(ctx, ctxK, v)
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// EntityID returns the entity_id claim injected by Authenticate.
func EntityID(ctx context.Context) string {
	v, _ := ctx.Value(ctxEntityID).(string)
	return v
}

// EntityRole returns the entity_role claim injected by Authenticate.
func EntityRole(ctx context.Context) string {
	v, _ := ctx.Value(ctxEntityRole).(string)
	return v
}

// SystemRole returns the system_role claim injected by Authenticate.
func SystemRole(ctx context.Context) string {
	v, _ := ctx.Value(ctxSystemRole).(string)
	return v
}

// UserID returns the sub (user ID) claim injected by Authenticate.
func UserID(ctx context.Context) string {
	v, _ := ctx.Value(ctxUserID).(string)
	return v
}
