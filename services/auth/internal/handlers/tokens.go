package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/veloci/auth/internal/tokens"
)

// TokenRow is the view of a token record exposed to handlers and test stubs.
type TokenRow struct {
	CredentialID string
	Claims       json.RawMessage
	ExpiresAt    time.Time
}

type tokenStore interface {
	StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time) error
	FindToken(ctx context.Context, jti string) (*TokenRow, error)
	DeleteToken(ctx context.Context, jti string) error
}

// Tokens handles token lifecycle HTTP endpoints.
type Tokens struct {
	db     tokenStore
	secret []byte
}

// NewTokens constructs a Tokens handler with the given store and signing secret.
func NewTokens(db tokenStore, secret []byte) *Tokens { return &Tokens{db: db, secret: secret} }

// Mint signs a new JWT and persists it to the token store.
func (h *Tokens) Mint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CredentialID string          `json:"credential_id"`
		Claims       json.RawMessage `json:"claims"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	jti := uuid.New().String()
	expiresAt := time.Now().Add(60 * time.Minute)

	tok, err := tokens.Mint(h.secret, jti, req.Claims, expiresAt)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}
	id := uuid.New().String()
	if err := h.db.StoreToken(r.Context(), id, req.CredentialID, jti, req.Claims, expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"token":      tok,
		"jti":        jti,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// Validate verifies a JWT signature/expiry and confirms it exists in the token store.
func (h *Tokens) Validate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	jti, _, err := tokens.Verify(h.secret, req.Token)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}
	row, err := h.db.FindToken(r.Context(), jti)
	if err != nil || time.Now().After(row.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jti":           jti,
		"credential_id": row.CredentialID,
		"claims":        json.RawMessage(row.Claims),
	})
}

// Revoke deletes a token record by jti, invalidating it immediately.
func (h *Tokens) Revoke(w http.ResponseWriter, r *http.Request) {
	jti := chi.URLParam(r, "jti")
	if err := h.db.DeleteToken(r.Context(), jti); err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
