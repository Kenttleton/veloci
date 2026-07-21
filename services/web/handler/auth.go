package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/labstack/echo/v4"
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

// Login validates credentials, looks up the user entity, and mints a JWT pair.
func (h *AuthHandler) Login(c echo.Context) error {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := c.Bind(&body); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid body")
	}

	ctx := c.Request().Context()

	cred, err := h.auth.ValidateCredential(ctx, &authclient.ValidateCredentialInputBody{
		Email:    body.Email,
		Password: body.Password,
	})
	if err != nil {
		log.Printf("login: ValidateCredential failed for %s: %v", body.Email, err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	ue, err := h.db.FindUserEntity(ctx, body.Email)
	if err != nil {
		log.Printf("login: FindUserEntity failed for %s: %v", body.Email, err)
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid credentials")
	}

	claims := make(authclient.MintTokenInputBodyClaims)
	for k, v := range map[string]any{
		"sub":         ue.UserID,
		"email":       body.Email,
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
		return echo.NewHTTPError(http.StatusInternalServerError, "internal error")
	}

	return c.JSON(http.StatusOK, map[string]string{
		"token":      minted.AccessToken,
		"expires_at": minted.ExpiresAt,
	})
}

// Logout revokes the caller's access token using the JTI injected by Authenticate middleware.
func (h *AuthHandler) Logout(c echo.Context) error {
	ctx := c.Request().Context()
	if jti := middleware.JTI(ctx); jti != "" {
		h.auth.RevokeToken(ctx, authclient.RevokeTokenParams{Jti: jti}) //nolint:errcheck
	}
	return c.NoContent(http.StatusNoContent)
}

// RegisterAuthRoutes registers public auth endpoints (no token required).
func RegisterAuthRoutes(g *echo.Group, h *AuthHandler) {
	g.POST("/auth/login", h.Login)
}

// RegisterLogoutRoute registers the logout endpoint on an authenticated API.
func RegisterLogoutRoute(g *echo.Group, h *AuthHandler) {
	g.POST("/auth/logout", h.Logout)
}
