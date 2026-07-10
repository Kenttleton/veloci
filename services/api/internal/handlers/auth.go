// Package handlers contains HTTP handlers for the veloci-api service.
package handlers

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/veloci/api/internal/authclient"
)

// UserEntity holds the resolved entity context for a user looked up in veloci_app.
type UserEntity struct {
	UserID     string
	EntityID   string
	EntityRole string
}

// appDB abstracts veloci_app database queries needed by the auth handler.
type appDB interface {
	FindUserEntity(ctx context.Context, email string) (UserEntity, error)
}

// Auth handles authentication-related HTTP requests.
type Auth struct {
	auth *authclient.Client
	db   appDB
}

// NewAuth creates a new Auth handler with the given ogen-generated auth client.
func NewAuth(auth *authclient.Client, db appDB) *Auth {
	return &Auth{auth: auth, db: db}
}

// Login validates credentials, looks up the user entity, and mints a JWT pair.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
		return
	}

	cred, err := h.auth.ValidateCredential(r.Context(), &authclient.ValidateCredentialInputBody{
		Email:    req.Email,
		Password: req.Password,
	})
	if err != nil {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
		return
	}

	ue, err := h.db.FindUserEntity(r.Context(), req.Email)
	if err != nil {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
		return
	}

	// Build claims as map[string]jx.Raw. jx.Raw is type Raw []byte, and []byte is
	// assignable to it — no jx import required.
	claims := make(authclient.MintTokenInputBodyClaims)
	for k, v := range map[string]any{
		"sub":         ue.UserID,
		"email":       req.Email,
		"system_role": string(cred.SystemRole),
		"entity_id":   ue.EntityID,
		"entity_role": ue.EntityRole,
	} {
		b, _ := json.Marshal(v)
		claims[k] = b
	}

	minted, err := h.auth.MintToken(r.Context(), &authclient.MintTokenInputBody{
		CredentialID: cred.CredentialID,
		Claims:       claims,
	})
	if err != nil {
		http.Error(w, `{"code":"INTERNAL"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"token":      minted.AccessToken,
		"expires_at": minted.ExpiresAt,
	})
}

// Logout revokes the token identified by the jti field in the request body.
func (h *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JTI string `json:"jti"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.JTI != "" {
		h.auth.RevokeToken(r.Context(), authclient.RevokeTokenParams{Jti: req.JTI}) //nolint:errcheck
	}
	w.WriteHeader(http.StatusNoContent)
}
