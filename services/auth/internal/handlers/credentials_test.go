package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/veloci/auth/internal/handlers"
	"golang.org/x/crypto/bcrypt"
)

type stubCredDB struct {
	hash              string
	role              string
	miss              bool
	updateFound       bool
	deleteFound       bool
	deleteRoleBlocked bool
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

func (s *stubCredDB) UpdateCredentialPassword(_ context.Context, id, hash string) (bool, error) {
	return s.updateFound, nil
}

func (s *stubCredDB) DeleteCredential(_ context.Context, id string) (bool, bool, error) {
	return s.deleteFound, s.deleteRoleBlocked, nil
}

// credRouter wires a chi+humachi router with credential routes registered.
func credRouter(db *stubCredDB) *chi.Mux {
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("test", "1.0.0"))
	handlers.RegisterCredentialRoutes(api, handlers.NewCredentials(db))
	return r
}

func TestValidateCredential_Success(t *testing.T) {
	hash, _ := bcrypt.GenerateFromPassword([]byte("correct"), 12)
	r := credRouter(&stubCredDB{hash: string(hash), role: "user"})

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "correct"})
	req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

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
	r := credRouter(&stubCredDB{hash: string(hash), role: "user"})

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/credentials/validate", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}

func TestCreate_Success(t *testing.T) {
	r := credRouter(&stubCredDB{})

	body, _ := json.Marshal(map[string]string{"email": "new@b.com", "password": "password123"})
	req := httptest.NewRequest(http.MethodPost, "/credentials/create", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("status: got %d want 201; body: %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["credential_id"] == "" {
		t.Error("expected credential_id in response")
	}
}

func TestUpdatePassword_NotFound(t *testing.T) {
	r := credRouter(&stubCredDB{updateFound: false})

	body, _ := json.Marshal(map[string]string{"password": "newpassword123"})
	req := httptest.NewRequest(http.MethodPut, "/credentials/missing-id/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d want 404; body: %s", w.Code, w.Body)
	}
}

func TestUpdatePassword_Success(t *testing.T) {
	r := credRouter(&stubCredDB{updateFound: true})

	body, _ := json.Marshal(map[string]string{"password": "newpassword123"})
	req := httptest.NewRequest(http.MethodPut, "/credentials/cred-1/password", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d want 204; body: %s", w.Code, w.Body)
	}
}

func TestDelete_NotFound(t *testing.T) {
	r := credRouter(&stubCredDB{deleteFound: false})

	req := httptest.NewRequest(http.MethodDelete, "/credentials/missing", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d want 404", w.Code)
	}
}

func TestDelete_ServerAdminForbidden(t *testing.T) {
	r := credRouter(&stubCredDB{deleteFound: true, deleteRoleBlocked: true})

	req := httptest.NewRequest(http.MethodDelete, "/credentials/admin-id", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Errorf("status: got %d want 403; body: %s", w.Code, w.Body)
	}
}

func TestDelete_Success(t *testing.T) {
	r := credRouter(&stubCredDB{deleteFound: true, deleteRoleBlocked: false})

	req := httptest.NewRequest(http.MethodDelete, "/credentials/cred-1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNoContent {
		t.Errorf("status: got %d want 204", w.Code)
	}
}
