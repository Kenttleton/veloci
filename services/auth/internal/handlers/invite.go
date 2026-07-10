package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"
)

type inviteStore interface {
	StoreInviteToken(ctx context.Context, tokenHash, createdBy string, claims []byte, expiresAt time.Time) error
	ConsumeInviteToken(ctx context.Context, tokenHash string) (found bool, alreadyConsumed bool, expired bool, err error)
}

// InviteConfig holds invite token configuration.
type InviteConfig struct {
	TTL time.Duration
}

// DefaultInviteConfig returns sensible production defaults.
func DefaultInviteConfig() InviteConfig {
	return InviteConfig{TTL: 72 * time.Hour}
}

// Invite handles invite token creation and consumption endpoints.
type Invite struct {
	db  inviteStore
	cfg InviteConfig
}

// NewInvite constructs an Invite handler with the given store and config.
func NewInvite(db inviteStore, cfg InviteConfig) *Invite {
	return &Invite{db: db, cfg: cfg}
}

// CreateInvite generates a new invite token, stores its SHA-256 hash, and returns the raw token.
func (h *Invite) CreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CreatedBy string          `json:"created_by"`
		Claims    json.RawMessage `json:"claims"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	if req.CreatedBy == "" || req.Claims == nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}

	// Generate 32 cryptographically random bytes, encode as URL-safe base64.
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL_ERROR"}`)
		return
	}
	rawToken := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := hashToken(rawToken)

	expiresAt := time.Now().Add(h.cfg.TTL)
	if err := h.db.StoreInviteToken(r.Context(), tokenHash, req.CreatedBy, []byte(req.Claims), expiresAt); err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL_ERROR"}`)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"token":      rawToken,
		"expires_at": expiresAt.UTC().Format(time.RFC3339),
	})
}

// ConsumeInvite atomically marks an invite token as accepted.
func (h *Invite) ConsumeInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	if req.Token == "" {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}

	hash := hashToken(req.Token)
	found, alreadyConsumed, expired, err := h.db.ConsumeInviteToken(r.Context(), hash)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL_ERROR"}`)
		return
	}
	if !found {
		writeJSON(w, http.StatusUnauthorized, `{"code":"UNAUTHORIZED"}`)
		return
	}
	if alreadyConsumed {
		writeJSON(w, http.StatusConflict, `{"code":"ALREADY_CONSUMED"}`)
		return
	}
	if expired {
		writeJSON(w, http.StatusGone, `{"code":"EXPIRED"}`)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
