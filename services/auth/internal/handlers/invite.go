package handlers

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
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

// ── Input / output types ──────────────────────────────────────────────────────

type CreateInviteInput struct {
	Body struct {
		CreatedBy string         `json:"created_by" required:"true" doc:"Credential UUID of the admin issuing the invite"`
		Claims    map[string]any `json:"claims"     required:"true" doc:"Claims to embed (e.g. email, entity_id, entity_role)"`
	}
}

type CreateInviteOutput struct {
	Body struct {
		Token     string `json:"token"      doc:"Raw URL-safe base64 token; returned once and never stored"`
		ExpiresAt string `json:"expires_at" doc:"Invite expiry as RFC 3339 timestamp"`
	}
}

type ConsumeInviteInput struct {
	Body struct {
		Token string `json:"token" required:"true" doc:"Raw URL-safe base64 invite token from the link"`
	}
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// CreateInvite generates a new invite token, stores its SHA-256 hash, and returns the raw token.
func (h *Invite) CreateInvite(ctx context.Context, input *CreateInviteInput) (*CreateInviteOutput, error) {
	claimsBytes, err := json.Marshal(input.Body.Claims)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid claims")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	rawToken := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := hashToken(rawToken)

	expiresAt := time.Now().Add(h.cfg.TTL)
	if err := h.db.StoreInviteToken(ctx, tokenHash, input.Body.CreatedBy, claimsBytes, expiresAt); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &CreateInviteOutput{}
	out.Body.Token = rawToken
	out.Body.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	return out, nil
}

// ConsumeInvite atomically marks an invite token as accepted.
func (h *Invite) ConsumeInvite(ctx context.Context, input *ConsumeInviteInput) (*struct{}, error) {
	hash := hashToken(input.Body.Token)
	found, alreadyConsumed, expired, err := h.db.ConsumeInviteToken(ctx, hash)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	if !found {
		return nil, huma.Error401Unauthorized("invite token not found")
	}
	if alreadyConsumed {
		return nil, huma.Error409Conflict("invite already consumed")
	}
	if expired {
		return nil, huma.Error410Gone("invite expired")
	}
	return nil, nil
}

// ── Route registration ────────────────────────────────────────────────────────

func RegisterInviteRoutes(api huma.API, h *Invite) {
	huma.Register(api, huma.Operation{
		OperationID:   "create-invite",
		Method:        http.MethodPost,
		Path:          "/invite",
		Summary:       "Create a one-time-use invite token",
		Description:   "The raw token is returned once and never stored. TTL from invite.ttl_hours in config (default 72h).",
		Tags:          []string{"invite"},
		DefaultStatus: http.StatusCreated,
	}, h.CreateInvite)

	huma.Register(api, huma.Operation{
		OperationID:   "consume-invite",
		Method:        http.MethodPost,
		Path:          "/invite/consume",
		Summary:       "Atomically consume an invite token",
		Description:   "Sets accepted_at only if unconsumed. This is the commit point in the invite saga.",
		Tags:          []string{"invite"},
		DefaultStatus: http.StatusNoContent,
	}, h.ConsumeInvite)
}
