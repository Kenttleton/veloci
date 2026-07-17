package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/middleware"
)

// UserEntity holds the resolved entity context for a user looked up in veloci_app.
type UserEntity struct {
	UserID     string
	EntityID   string
	EntityRole string
}

// AppDB abstracts veloci_app database queries needed by the auth handler.
type AppDB interface {
	FindUserEntity(ctx context.Context, email string) (UserEntity, error)
}

// AuthHandler handles authentication-related HTTP requests.
type AuthHandler struct {
	auth *authclient.Client
	db   AppDB
}

// NewAuthHandler creates an AuthHandler with the given ogen-generated auth client.
func NewAuthHandler(auth *authclient.Client, db AppDB) *AuthHandler {
	return &AuthHandler{auth: auth, db: db}
}

type loginInput struct {
	Body struct {
		Email    string `json:"email"    required:"true" doc:"User email address"`
		Password string `json:"password" required:"true" doc:"Plaintext password"`
	}
}

type loginOutput struct {
	Body struct {
		Token     string `json:"token"      doc:"Short-lived access token"`
		ExpiresAt string `json:"expires_at" doc:"Token expiry as RFC 3339 timestamp"`
	}
}

// Login validates credentials, looks up the user entity, and mints a JWT pair.
func (h *AuthHandler) Login(ctx context.Context, input *loginInput) (*loginOutput, error) {
	cred, err := h.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    input.Body.Email,
		Password: input.Body.Password,
	})
	if err != nil {
		log.Printf("login: ValidateCredential failed for %s: %v", input.Body.Email, err)
		return nil, huma.Error401Unauthorized("invalid credentials")
	}

	ue, err := h.db.FindUserEntity(ctx, input.Body.Email)
	if err != nil {
		log.Printf("login: FindUserEntity failed for %s: %v", input.Body.Email, err)
		return nil, huma.Error401Unauthorized("invalid credentials")
	}

	claims := make(authclient.MintTokenInputBodyClaims)
	for k, v := range map[string]any{
		"sub":         ue.UserID,
		"email":       input.Body.Email,
		"system_role": string(cred.SystemRole),
		"entity_id":   ue.EntityID,
		"entity_role": ue.EntityRole,
	} {
		b, _ := json.Marshal(v)
		claims[k] = b
	}

	minted, err := h.auth.MintToken(ctx, &authclient.MintTokenInputBody{
		CredentialID: cred.CredentialID,
		Claims:       claims,
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &loginOutput{}
	out.Body.Token = minted.AccessToken
	out.Body.ExpiresAt = minted.ExpiresAt
	return out, nil
}

// Logout revokes the caller's access token using the JTI injected by Authenticate middleware.
func (h *AuthHandler) Logout(ctx context.Context, input *struct{}) (*struct{}, error) {
	if jti := middleware.JTI(ctx); jti != "" {
		h.auth.RevokeToken(ctx, authclient.RevokeTokenParams{Jti: jti}) //nolint:errcheck
	}
	return nil, nil
}

// RegisterAuthRoutes registers public auth endpoints (no token required).
func RegisterAuthRoutes(api huma.API, h *AuthHandler) {
	huma.Register(api, huma.Operation{
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/auth/login",
		Summary:     "Login with email and password",
		Tags:        []string{"auth"},
	}, h.Login)
}

// RegisterLogoutRoute registers the logout endpoint on an authenticated API.
func RegisterLogoutRoute(api huma.API, h *AuthHandler) {
	huma.Register(api, huma.Operation{
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/auth/logout",
		Summary:       "Revoke the caller's access token",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
	}, h.Logout)
}
