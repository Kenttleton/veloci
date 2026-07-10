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

// NewAuth creates a new Auth handler.
func NewAuth(authURL string, db appDB) *Auth {
	return &Auth{auth: authclient.New(authURL), db: db}
}

// Login validates credentials, looks up the user entity, and mints a JWT.
func (h *Auth) Login(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"code":"BAD_REQUEST"}`, http.StatusBadRequest)
		return
	}

	cred, err := h.auth.ValidateCredential(r.Context(), req.Email, req.Password)
	if err != nil {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
		return
	}

	ue, err := h.db.FindUserEntity(r.Context(), req.Email)
	if err != nil {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
		return
	}

	claims := map[string]any{
		"sub":         ue.UserID,
		"email":       req.Email,
		"system_role": cred.SystemRole,
		"entity_id":   ue.EntityID,
		"entity_role": ue.EntityRole,
	}
	minted, err := h.auth.MintToken(r.Context(), cred.CredentialID, claims)
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
		h.auth.RevokeToken(r.Context(), req.JTI)
	}
	w.WriteHeader(http.StatusNoContent)
}
