package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
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

// ── Input / output types ──────────────────────────────────────────────────────

type MintTokenInput struct {
	Body struct {
		CredentialID string         `json:"credential_id" required:"true" doc:"UUID of the credential to bind this token to"`
		Claims       map[string]any `json:"claims"        required:"true" doc:"Opaque claims set by veloci-api; stored verbatim"`
	}
}

// TokenPairOutput is the response shape for mint and refresh.
type TokenPairOutput struct {
	Body struct {
		AccessToken  string `json:"access_token"  doc:"Signed HS256 JWT; valid for session.access_token_ttl_minutes"`
		RefreshToken string `json:"refresh_token" doc:"Signed HS256 JWT; valid for session.refresh_token_ttl_hours"`
		JTI          string `json:"jti"           doc:"JTI of the access token"`
		ExpiresIn    int    `json:"expires_in"    doc:"Access token lifetime in seconds"`
		ExpiresAt    string `json:"expires_at"    doc:"Access token expiry as RFC 3339 timestamp"`
	}
}

type RefreshTokenInput struct {
	Body struct {
		RefreshToken string `json:"refresh_token" required:"true" doc:"The refresh JWT to exchange for a new pair"`
	}
}

type ValidateTokenInput struct {
	Body struct {
		Token string `json:"token" required:"true" doc:"Raw token — JWT (three dot-separated base64url segments) or OTU (flat base64url)"`
	}
}

type ValidateTokenOutput struct {
	Body struct {
		TokenType    string         `json:"token_type"              enum:"access,invite" doc:"Token type from the validated record"`
		JTI          string         `json:"jti,omitempty"           doc:"Access token JTI. Present only when token_type is access"`
		CredentialID string         `json:"credential_id,omitempty" doc:"Credential UUID. Present only when token_type is access"`
		Claims       map[string]any `json:"claims"                  doc:"Opaque claims from storage; DB-authoritative"`
	}
}

type RevokeTokenInput struct {
	JTI string `path:"jti" doc:"Token JTI"`
}

type RevokeUserTokensInput struct {
	CredentialID string `path:"credential_id" doc:"Credential UUID"`
}

// ── Internal helpers ──────────────────────────────────────────────────────────

// mintPair issues an access+refresh token pair and stores both in the DB.
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

// ── Handlers ──────────────────────────────────────────────────────────────────

// Mint signs a new access+refresh JWT pair and persists both to the token store.
func (h *Tokens) Mint(ctx context.Context, input *MintTokenInput) (*TokenPairOutput, error) {
	claimsBytes, err := json.Marshal(input.Body.Claims)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid claims")
	}

	accessTok, refreshTok, accessJTI, accessExp, err := h.mintPair(ctx, input.Body.CredentialID, claimsBytes)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &TokenPairOutput{}
	out.Body.AccessToken = accessTok
	out.Body.RefreshToken = refreshTok
	out.Body.JTI = accessJTI
	out.Body.ExpiresIn = int(h.cfg.AccessTTL.Seconds())
	out.Body.ExpiresAt = accessExp.UTC().Format(time.RFC3339)
	return out, nil
}

// Validate verifies a token — JWT or OTU invite — and confirms it exists in the appropriate store.
// Token type is detected by structure: JWTs have two dots; OTU tokens do not.
func (h *Tokens) Validate(ctx context.Context, input *ValidateTokenInput) (*ValidateTokenOutput, error) {
	if strings.Count(input.Body.Token, ".") == 2 {
		return h.validateJWT(ctx, input.Body.Token)
	}
	return h.validateInviteToken(ctx, input.Body.Token)
}

func (h *Tokens) validateJWT(ctx context.Context, tokenStr string) (*ValidateTokenOutput, error) {
	jti, tokenType, _, err := tokens.Verify(h.secret, tokenStr)
	if err != nil {
		return nil, huma.Error401Unauthorized("unauthorized")
	}
	if tokenType == "refresh" {
		return nil, huma.Error401Unauthorized("unauthorized")
	}

	// DB claims are authoritative; JWT claims used only to extract jti for DB lookup.
	row, err := h.db.FindToken(ctx, jti)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("unauthorized")
		}
		return nil, huma.Error401Unauthorized("unauthorized")
	}
	if time.Now().After(row.ExpiresAt) {
		return nil, huma.Error401Unauthorized("unauthorized")
	}

	var claims map[string]any
	if err := json.Unmarshal(row.Claims, &claims); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &ValidateTokenOutput{}
	out.Body.TokenType = "access"
	out.Body.JTI = jti
	out.Body.CredentialID = row.CredentialID
	out.Body.Claims = claims
	return out, nil
}

func (h *Tokens) validateInviteToken(ctx context.Context, rawToken string) (*ValidateTokenOutput, error) {
	hash := hashToken(rawToken)
	row, err := h.db.FindInviteToken(ctx, hash)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("unauthorized")
		}
		return nil, huma.Error401Unauthorized("unauthorized")
	}
	if row.AcceptedAt != nil || time.Now().After(row.ExpiresAt) {
		return nil, huma.Error401Unauthorized("unauthorized")
	}

	var claims map[string]any
	if err := json.Unmarshal(row.Claims, &claims); err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &ValidateTokenOutput{}
	out.Body.TokenType = "invite"
	out.Body.Claims = claims
	return out, nil
}

// Refresh validates a refresh token and issues a new access+refresh pair.
func (h *Tokens) Refresh(ctx context.Context, input *RefreshTokenInput) (*TokenPairOutput, error) {
	jti, tokenType, _, err := tokens.Verify(h.secret, input.Body.RefreshToken)
	if err != nil {
		return nil, huma.Error401Unauthorized("refresh token invalid")
	}
	if tokenType != "refresh" {
		return nil, huma.Error401Unauthorized("refresh token invalid")
	}

	row, err := h.db.FindToken(ctx, jti)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("refresh token invalid")
		}
		return nil, huma.Error401Unauthorized("refresh token invalid")
	}
	if time.Now().After(row.ExpiresAt) {
		return nil, huma.Error401Unauthorized("refresh token expired")
	}

	const graceWindow = 30 * time.Second
	if err := h.db.RotateRefreshToken(ctx, jti, graceWindow); err != nil {
		if errors.Is(err, db.ErrReplayDetected) || errors.Is(err, db.ErrTokenNotFound) {
			return nil, huma.Error401Unauthorized("refresh token invalid")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}

	accessTok, refreshTok, accessJTI, accessExp, err := h.mintPair(ctx, row.CredentialID, json.RawMessage(`{}`))
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}

	out := &TokenPairOutput{}
	out.Body.AccessToken = accessTok
	out.Body.RefreshToken = refreshTok
	out.Body.JTI = accessJTI
	out.Body.ExpiresIn = int(h.cfg.AccessTTL.Seconds())
	out.Body.ExpiresAt = accessExp.UTC().Format(time.RFC3339)
	return out, nil
}

// Revoke deletes a token record by jti. Always 204 — idempotent.
func (h *Tokens) Revoke(ctx context.Context, input *RevokeTokenInput) (*struct{}, error) {
	h.db.DeleteToken(ctx, input.JTI) //nolint:errcheck
	return nil, nil
}

// RevokeUser removes all active token records for a credential without deleting the credential.
func (h *Tokens) RevokeUser(ctx context.Context, input *RevokeUserTokensInput) (*struct{}, error) {
	h.db.DeleteUserTokens(ctx, input.CredentialID) //nolint:errcheck
	return nil, nil
}

// ── Route registration ────────────────────────────────────────────────────────

func RegisterTokenRoutes(api huma.API, h *Tokens) {
	huma.Register(api, huma.Operation{
		OperationID:   "mint-token",
		Method:        http.MethodPost,
		Path:          "/tokens/mint",
		Summary:       "Mint an access+refresh token pair",
		Tags:          []string{"tokens"},
		DefaultStatus: http.StatusCreated,
	}, h.Mint)

	huma.Register(api, huma.Operation{
		OperationID: "validate-token",
		Method:      http.MethodPost,
		Path:        "/tokens/validate",
		Summary:     "Validate any token — JWT or OTU",
		Description: "Auth detects token type by structure (JWT = two dots; OTU = no dots) and queries the appropriate table.",
		Tags:        []string{"tokens"},
	}, h.Validate)

	huma.Register(api, huma.Operation{
		OperationID: "refresh-token",
		Method:      http.MethodPost,
		Path:        "/tokens/refresh",
		Summary:     "Exchange a refresh token for a new access+refresh pair",
		Description: "Rotates the refresh token within a 30-second grace window to handle concurrent requests.",
		Tags:        []string{"tokens"},
	}, h.Refresh)

	// Register /tokens/user/{credential_id} before /tokens/{jti} — chi's radix tree
	// prioritises the static "user" segment, but explicit ordering makes intent clear.
	huma.Register(api, huma.Operation{
		OperationID:   "revoke-user-tokens",
		Method:        http.MethodDelete,
		Path:          "/tokens/user/{credential_id}",
		Summary:       "Revoke all sessions for a credential without deleting it",
		Tags:          []string{"tokens"},
		DefaultStatus: http.StatusNoContent,
	}, h.RevokeUser)

	huma.Register(api, huma.Operation{
		OperationID:   "revoke-token",
		Method:        http.MethodDelete,
		Path:          "/tokens/{jti}",
		Summary:       "Revoke a single token by JTI (idempotent)",
		Tags:          []string{"tokens"},
		DefaultStatus: http.StatusNoContent,
	}, h.Revoke)
}
