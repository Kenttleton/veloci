package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/auth/internal/handlers"
	"golang.org/x/crypto/bcrypt"
)

type stubCredDB struct {
	hash string
	role string
	miss bool
}

func (s *stubCredDB) FindCredentialByEmail(_ context.Context, _ string) (*handlers.CredentialRow, error) {
	if s.miss {
		return nil, handlers.ErrNotFound
	}
	return &handlers.CredentialRow{ID: "cred-1", PasswordHash: s.hash, SystemRole: s.role}, nil
}

func (s *stubCredDB) CreateCredential(_ context.Context, id, email, hash, role string) error {
	return nil
}

func TestValidateCredential_Success(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), 12)
	db := &stubCredDB{hash: string(hash), role: "user"}
	h := handlers.NewCredentials(db)

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "correct"})
	req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Validate(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200; body: %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["credential_id"] != "cred-1" {
		t.Errorf("credential_id: got %q", resp["credential_id"])
	}
	if resp["system_role"] != "user" {
		t.Errorf("system_role: got %q", resp["system_role"])
	}
}

func TestValidateCredential_WrongPassword(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), 12)
	db := &stubCredDB{hash: string(hash), role: "user"}
	h := handlers.NewCredentials(db)

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	h.Validate(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
