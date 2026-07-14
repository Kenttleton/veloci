package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/veloci/api/authclient"
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

type logoutInput struct {
	Body struct {
		JTI string `json:"jti" required:"true" doc:"Access token JTI to revoke"`
	}
}

// Login validates credentials, looks up the user entity, and mints a JWT pair.
func (h *AuthHandler) Login(ctx context.Context, input *loginInput) (*loginOutput, error) {
	cred, err := h.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    input.Body.Email,
		Password: input.Body.Password,
	})
	if err != nil {
		return nil, huma.Error401Unauthorized("invalid credentials")
	}

	ue, err := h.db.FindUserEntity(ctx, input.Body.Email)
	if err != nil {
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

// Logout revokes the token identified by the jti field in the request body.
func (h *AuthHandler) Logout(ctx context.Context, input *logoutInput) (*struct{}, error) {
	h.auth.RevokeToken(ctx, authclient.RevokeTokenParams{Jti: input.Body.JTI}) //nolint:errcheck
	return nil, nil
}

// RegisterAuthRoutes registers auth endpoints on the given Huma API.
func RegisterAuthRoutes(api huma.API, h *AuthHandler) {
	huma.Register(api, huma.Operation{
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/auth/login",
		Summary:     "Login with email and password",
		Tags:        []string{"auth"},
	}, h.Login)

	huma.Register(api, huma.Operation{
		OperationID:   "logout",
		Method:        http.MethodPost,
		Path:          "/auth/logout",
		Summary:       "Revoke an access token",
		Tags:          []string{"auth"},
		DefaultStatus: http.StatusNoContent,
	}, h.Logout)
}
