package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/veloci/api/authclient"
)

type contextKey string

const (
	ctxEntityID   contextKey = "entity_id"
	ctxEntityRole contextKey = "entity_role"
	ctxSystemRole contextKey = "system_role"
	ctxUserID     contextKey = "sub"
)

// Authenticate validates the Bearer token via veloci-auth /tokens/validate.
// Only access tokens are accepted — invite tokens are rejected with 401.
// Verified claims (entity_id, entity_role, system_role, sub) are injected into context.
func Authenticate(client *authclient.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if !strings.HasPrefix(header, "Bearer ") {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			result, err := client.ValidateToken(r.Context(), &authclient.ValidateTokenInputBody{
				Token: strings.TrimPrefix(header, "Bearer "),
			})
			if err != nil {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			if result.TokenType != authclient.ValidateTokenOutputBodyTokenTypeAccess {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}

			ctx := r.Context()
			for k, raw := range result.Claims {
				var s string
				if json.Unmarshal(raw, &s) != nil {
					continue
				}
				switch k {
				case "entity_id":
					ctx = context.WithValue(ctx, ctxEntityID, s)
				case "entity_role":
					ctx = context.WithValue(ctx, ctxEntityRole, s)
				case "system_role":
					ctx = context.WithValue(ctx, ctxSystemRole, s)
				case "sub":
					ctx = context.WithValue(ctx, ctxUserID, s)
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
