package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// ErrNotFound is returned by stub implementations in tests when a record is missing.
var ErrNotFound = errors.New("not found")

// CredentialRow is the view of a credential row exposed to handlers and test stubs.
type CredentialRow struct {
	ID           string
	PasswordHash string
	SystemRole   string
}

type credentialStore interface {
	FindCredentialByEmail(ctx context.Context, email string) (*CredentialRow, error)
	CreateCredential(ctx context.Context, id, email, hash, role string) error
}

// Credentials handles credential-related HTTP endpoints.
type Credentials struct{ db credentialStore }

// NewCredentials constructs a Credentials handler with the given store.
func NewCredentials(db credentialStore) *Credentials { return &Credentials{db: db} }

// Validate checks email+password and returns credential_id and system_role on success.
func (h *Credentials) Validate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	cred, err := h.db.FindCredentialByEmail(r.Context(), req.Email)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusUnauthorized, `{"code":"INVALID_CREDENTIALS"}`)
			return
		}
		writeJSON(w, http.StatusUnauthorized, `{"code":"INVALID_CREDENTIALS"}`)
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(cred.PasswordHash), []byte(req.Password)) != nil {
		writeJSON(w, http.StatusUnauthorized, `{"code":"INVALID_CREDENTIALS"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"credential_id": cred.ID,
		"system_role":   cred.SystemRole,
	})
}

// Create registers a new credential with system_role "user".
func (h *Credentials) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, `{"code":"BAD_REQUEST"}`)
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, `{"code":"INTERNAL"}`)
		return
	}
	id := uuid.New().String()
	if err := h.db.CreateCredential(r.Context(), id, req.Email, string(hash), "user"); err != nil {
		writeJSON(w, http.StatusConflict, `{"code":"CONFLICT"}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"credential_id": id})
}
