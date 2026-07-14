package credentials

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/veloci/auth/store"
	"golang.org/x/crypto/bcrypt"
)

// ErrNotFound is returned by test stubs when a record is missing.
var ErrNotFound = errors.New("not found")

// ErrForbidden is returned when an operation is not permitted on the target record.
var ErrForbidden = errors.New("forbidden")

type credentialStore interface {
	FindCredentialByEmail(ctx context.Context, email string) (*store.Credential, error)
	CreateCredential(ctx context.Context, id, email, hash, role string) error
	UpdateCredentialPassword(ctx context.Context, id, hash string) (found bool, err error)
	DeleteCredential(ctx context.Context, id string) (found bool, systemRoleBlocked bool, err error)
}

// Handler handles credential-related HTTP endpoints.
type Handler struct{ db credentialStore }

// NewHandler constructs a Handler with the given store.
func NewHandler(db credentialStore) *Handler { return &Handler{db: db} }

// ── Input / output types ──────────────────────────────────────────────────────

type ValidateCredentialInput struct {
	Body struct {
		Email    string `json:"email"    required:"true" doc:"User email address"`
		Password string `json:"password" required:"true" doc:"Plaintext password"`
	}
}
type ValidateCredentialOutput struct {
	Body struct {
		CredentialID string `json:"credential_id" doc:"Credential UUID used as token subject"`
		SystemRole   string `json:"system_role"   enum:"server_admin,user"`
	}
}

type CreateCredentialInput struct {
	Body struct {
		Email    string `json:"email"    required:"true" doc:"User email address"`
		Password string `json:"password" required:"true" minLength:"8" doc:"Plaintext password; bcrypt hashed at cost 12"`
	}
}
type CreateCredentialOutput struct {
	Body struct {
		CredentialID string `json:"credential_id" doc:"UUID of the created credential"`
	}
}

type UpdateCredentialPasswordInput struct {
	ID   string `path:"id" doc:"Credential UUID"`
	Body struct {
		Password string `json:"password" required:"true" doc:"New plaintext password"`
	}
}

type DeleteCredentialInput struct {
	ID string `path:"id" doc:"Credential UUID"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

// Validate checks email+password and returns credential_id and system_role on success.
func (h *Handler) Validate(ctx context.Context, input *ValidateCredentialInput) (*ValidateCredentialOutput, error) {
	cred, err := h.db.FindCredentialByEmail(ctx, input.Body.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			return nil, huma.Error401Unauthorized("invalid credentials")
		}
		return nil, huma.Error401Unauthorized("invalid credentials")
	}
	if bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(input.Body.Password)) != nil {
		return nil, huma.Error401Unauthorized("invalid credentials")
	}
	out := &ValidateCredentialOutput{}
	out.Body.CredentialID = cred.ID
	out.Body.SystemRole = cred.SystemRole
	return out, nil
}

// Create registers a new credential with system_role "user".
func (h *Handler) Create(ctx context.Context, input *CreateCredentialInput) (*CreateCredentialOutput, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Body.Password), 12)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	id := uuid.New().String()
	if err := h.db.CreateCredential(ctx, id, input.Body.Email, string(hash), "user"); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "auth_credentials_email_key" {
			return nil, huma.Error409Conflict("email already registered")
		}
		return nil, huma.Error500InternalServerError("internal error")
	}
	out := &CreateCredentialOutput{}
	out.Body.CredentialID = id
	return out, nil
}

// UpdatePassword replaces the password hash for an existing credential.
func (h *Handler) UpdatePassword(ctx context.Context, input *UpdateCredentialPasswordInput) (*struct{}, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Body.Password), 12)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	found, err := h.db.UpdateCredentialPassword(ctx, input.ID, string(hash))
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	if !found {
		return nil, huma.Error404NotFound("credential not found")
	}
	return nil, nil
}

// Delete permanently removes a credential and all its tokens via FK cascade.
func (h *Handler) Delete(ctx context.Context, input *DeleteCredentialInput) (*struct{}, error) {
	found, systemRoleBlocked, err := h.db.DeleteCredential(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("internal error")
	}
	if !found {
		return nil, huma.Error404NotFound("credential not found")
	}
	if systemRoleBlocked {
		return nil, huma.Error403Forbidden("cannot delete server_admin credential")
	}
	return nil, nil
}

// ── Route registration ────────────────────────────────────────────────────────

func RegisterRoutes(api huma.API, h *Handler) {
	huma.Register(api, huma.Operation{
		OperationID: "validate-credential",
		Method:      http.MethodPost,
		Path:        "/credentials/validate",
		Summary:     "Validate email/password credentials",
		Tags:        []string{"credentials"},
	}, h.Validate)

	huma.Register(api, huma.Operation{
		OperationID:   "create-credential",
		Method:        http.MethodPost,
		Path:          "/credentials/create",
		Summary:       "Create a new credential",
		Tags:          []string{"credentials"},
		DefaultStatus: http.StatusCreated,
	}, h.Create)

	huma.Register(api, huma.Operation{
		OperationID:   "update-credential-password",
		Method:        http.MethodPut,
		Path:          "/credentials/{id}/password",
		Summary:       "Update password hash for a credential",
		Tags:          []string{"credentials"},
		DefaultStatus: http.StatusNoContent,
	}, h.UpdatePassword)

	huma.Register(api, huma.Operation{
		OperationID:   "delete-credential",
		Method:        http.MethodDelete,
		Path:          "/credentials/{id}",
		Summary:       "Permanently remove a credential and cascade tokens",
		Tags:          []string{"credentials"},
		DefaultStatus: http.StatusNoContent,
	}, h.Delete)
}
