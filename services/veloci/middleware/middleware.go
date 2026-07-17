package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/veloci/veloci/authclient"
)

type contextKey string

const (
	ctxEntityID   contextKey = "entity_id"
	ctxEntityRole contextKey = "entity_role"
	ctxSystemRole contextKey = "system_role"
	ctxUserID     contextKey = "sub"
	ctxJTI        contextKey = "jti"
)

// validateToken calls veloci-auth to verify a raw token string and injects
// claims into the request context. Returns false and writes 401 on failure.
func validateToken(client *authclient.Client, token string, w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	result, err := client.ValidateToken(r.Context(), &authclient.ValidateTokenInputBody{
		Token: token,
	})
	if err != nil {
		http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
		return r, false
	}
	if result.TokenType != authclient.ValidateTokenOutputBodyTokenTypeAccess {
		http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
		return r, false
	}

	ctx := r.Context()
	if jti, ok := result.GetJti().Get(); ok {
		ctx = context.WithValue(ctx, ctxJTI, jti)
	}
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
	return r.WithContext(ctx), true
}

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
			token := strings.TrimPrefix(header, "Bearer ")
			req, ok := validateToken(client, token, w, r)
			if !ok {
				return
			}
			next.ServeHTTP(w, req)
		})
	}
}

// AuthenticateSSE validates a token passed as the ?token= query parameter.
// Used exclusively for the SSE endpoint, which cannot send Authorization headers.
func AuthenticateSSE(client *authclient.Client) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := r.URL.Query().Get("token")
			if token == "" {
				http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
				return
			}
			req, ok := validateToken(client, token, w, r)
			if !ok {
				return
			}
			next.ServeHTTP(w, req)
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

// JTI returns the access token JTI injected by Authenticate.
func JTI(ctx context.Context) string {
	v, _ := ctx.Value(ctxJTI).(string)
	return v
}
