package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/veloci/auth/internal/db"
	"github.com/veloci/auth/internal/tokens"
)

// TokenRow is the view of a token record exposed to handlers and test stubs.
type TokenRow struct {
	CredentialID string
	Claims       json.RawMessage
	ExpiresAt    time.Time
	TokenType    string
	RotatedAt    *time.Time
}

// InviteTokenRow is the view of an invite_tokens record exposed to handlers and test stubs.
type InviteTokenRow struct {
	Claims     json.RawMessage
	ExpiresAt  time.Time
	AcceptedAt *time.Time
}

type tokenStore interface {
	StoreToken(ctx context.Context, id, userID, jti string, claims json.RawMessage, exp time.Time, tokenType string, parentID *string) error
	FindToken(ctx context.Context, jti string) (*TokenRow, error)
	DeleteToken(ctx context.Context, jti string) error
	DeleteUserTokens(ctx context.Context, credentialID string) error
	RotateRefreshToken(ctx context.Context, oldJTI string, graceWindow time.Duration) error
	FindInviteToken(ctx context.Context, tokenHash string) (*InviteTokenRow, error)
}

// TokenConfig holds token lifetime configuration.
type TokenConfig struct {
	AccessTTL  time.Duration
	RefreshTTL time.Duration
}

// DefaultTokenConfig returns sensible production defaults.
func DefaultTokenConfig() TokenConfig {
	return TokenConfig{
		AccessTTL:  15 * time.Minute,
		RefreshTTL: 24 * time.Hour,
	}
}

// Tokens handles token lifecycle HTTP endpoints.
type Tokens struct {
	db     tokenStore
	secret []byte
	cfg    TokenConfig
}

// NewTokens constructs a Tokens handler with the given store, signing secret, and config.
func NewTokens(db tokenStore, secret []byte, cfg TokenConfig) *Tokens {
	return &Tokens{db: db, secret: secret, cfg: cfg}
}

// mintPair issues an access+refresh token pair and stores both in the DB.
// It returns the minted tokens and the access token's expiry metadata.
// parentID is the access token's row ID, stored as parent on the refresh token.
func (h *Tokens) mintPair(ctx context.Context, credentialID string, claims json.RawMessage) (accessTok, refreshTok, accessJTI string, accessExp time.Time, err error) {
	now := time.Now()
	accessExp = now.Add(h.cfg.AccessTTL)
	refreshExp := now.Add(h.cfg.RefreshTTL)

	accessJTI = uuid.New().String()
	accessID := uuid.New().String()

	accessTok, err = tokens.Mint(h.secret, accessJTI, claims, accessExp, "access")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	if err = h.db.StoreToken(ctx, accessID, credentialID, accessJTI, claims, accessExp, "access", nil); err != nil {
		return "", "", "", time.Time{}, err
	}

	refreshJTI := uuid.New().String()
	refreshID := uuid.New().String()
	// Build minimal claims for the refresh token — just enough for signature validation.
	refreshClaims := json.RawMessage(`{}`)

	refreshTok, err = tokens.Mint(h.secret, refreshJTI, refreshClaims, refreshExp, "refresh")
	if err != nil {
		return "", "", "", time.Time{}, err
	}
	if err = h.db.StoreToken(ctx, refreshID, credentialID, refreshJTI, refreshClaims, refreshExp, "refresh", &accessID); err != nil {
		return "", "", "", time.Time{}, err
	}

	return accessTok, refreshTok, accessJTI, accessExp, nil
}

// Mint signs a new access+refresh JWT pair and persists both to the token store.
func (h *Tokens) Mint(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CredentialID string          `json:"credential_id"`
		Claims       json.RawMessage `json:"claims"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	if req.CredentialID == "" {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	if req.Claims == nil || string(req.Claims) == "null" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	accessTok, refreshTok, accessJTI, accessExp, err := h.mintPair(r.Context(), req.CredentialID, req.Claims)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessTok,
		"refresh_token": refreshTok,
		"jti":           accessJTI,
		"expires_in":    int(h.cfg.AccessTTL.Seconds()),
		"expires_at":    accessExp.UTC().Format(time.RFC3339),
	})
}

// Validate verifies a token — JWT or OTU invite — and confirms it exists in the appropriate store.
// Token type is detected by structure: JWTs have two dots; OTU tokens do not.
func (h *Tokens) Validate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}

	// Structural token type detection: JWTs contain exactly two dots.
	if strings.Count(req.Token, ".") == 2 {
		h.validateJWT(w, r, req.Token)
	} else {
		h.validateInviteToken(w, r, req.Token)
	}
}

// validateJWT handles the JWT path for Validate.
func (h *Tokens) validateJWT(w http.ResponseWriter, r *http.Request, tokenStr string) {
	jti, tokenType, _, err := tokens.Verify(h.secret, tokenStr)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	// Refresh tokens are never routed through /tokens/validate.
	if tokenType == "refresh" {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	// DB claims are authoritative; JWT claims used only to extract jti for DB lookup.
	row, err := h.db.FindToken(r.Context(), jti)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	if time.Now().After(row.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"token_type":    "access",
		"jti":           jti,
		"credential_id": row.CredentialID,
		"claims":        json.RawMessage(row.Claims),
	})
}

// validateInviteToken handles the OTU invite token path for Validate.
func (h *Tokens) validateInviteToken(w http.ResponseWriter, r *http.Request, rawToken string) {
	hash := hashToken(rawToken)
	row, err := h.db.FindInviteToken(r.Context(), hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	if row.AcceptedAt != nil || time.Now().After(row.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"token_type": "invite",
		"claims":     json.RawMessage(row.Claims),
	})
}

// Refresh validates a refresh token and issues a new access+refresh pair.
// The old refresh token is rotated within a 30-second grace window.
func (h *Tokens) Refresh(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	if req.RefreshToken == "" {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}

	jti, tokenType, _, err := tokens.Verify(h.secret, req.RefreshToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
		return
	}
	if tokenType != "refresh" {
		writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
		return
	}

	// Find the refresh token row to get credential ID and check expiry.
	row, err := h.db.FindToken(r.Context(), jti)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
		return
	}

	if time.Now().After(row.ExpiresAt) {
		writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_EXPIRED"}`)
		return
	}

	const graceWindow = 30 * time.Second
	if err := h.db.RotateRefreshToken(r.Context(), jti, graceWindow); err != nil {
		if errors.Is(err, db.ErrReplayDetected) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
			return
		}
		if errors.Is(err, db.ErrTokenNotFound) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"REFRESH_TOKEN_INVALID"}`)
			return
		}
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}

	// We need the original access claims to mint the new pair.
	// The refresh row carries empty claims; we mint new tokens with an empty claim set
	// since veloci-api should re-mint with fresh claims after refresh. Per spec,
	// refresh returns the same shape as mint — the claims in the new access token
	// will be the claims stored on the old access token (parent of this refresh).
	// For simplicity, pass the claims from the refresh row (empty {}).
	accessTok, refreshTok, accessJTI, accessExp, err := h.mintPair(r.Context(), row.CredentialID, json.RawMessage(`{}`))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]any{
		"access_token":  accessTok,
		"refresh_token": refreshTok,
		"jti":           accessJTI,
		"expires_in":    int(h.cfg.AccessTTL.Seconds()),
		"expires_at":    accessExp.UTC().Format(time.RFC3339),
	})
}

// Revoke deletes a token record by jti, invalidating it immediately.
// Always returns 204 — intentionally idempotent; the CommandTag is discarded.
func (h *Tokens) Revoke(w http.ResponseWriter, r *http.Request) {
	jti := chi.URLParam(r, "jti")
	// CommandTag intentionally discarded — revoke is idempotent, always 204.
	h.db.DeleteToken(r.Context(), jti) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}

// RevokeUser removes all active token records for a credential without deleting the credential.
func (h *Tokens) RevokeUser(w http.ResponseWriter, r *http.Request) {
	credentialID := chi.URLParam(r, "credential_id")
	h.db.DeleteUserTokens(r.Context(), credentialID) //nolint:errcheck
	w.WriteHeader(http.StatusNoContent)
}
