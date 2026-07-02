package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/api/internal/handlers"
)

// stubAuthForLogin simulates veloci-auth /credentials/validate and /tokens/mint.
func stubAuthForLogin(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/credentials/validate":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"credential_id": "cred-1",
				"system_role":   "user",
			})
		case "/tokens/mint":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{
				"token":      "test-token",
				"jti":        "jti-1",
				"expires_at": "2099-01-01T00:00:00Z",
			})
		default:
			t.Errorf("unexpected auth call: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
}

// stubAppDB simulates the veloci_app lookup for entity+role.
type stubAppDB struct{}

func (s *stubAppDB) FindUserEntity(ctx context.Context, email string) (handlers.UserEntity, error) {
	return handlers.UserEntity{UserID: "user-1", EntityID: "ent-1", EntityRole: "entity_admin"}, nil
}

func TestLoginSuccess(t *testing.T) {
	authSrv := stubAuthForLogin(t)
	defer authSrv.Close()

	h := handlers.NewAuth(authSrv.URL, &stubAppDB{})
	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d; body: %s", w.Code, w.Body)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["token"] == "" {
		t.Error("expected token in response")
	}
	if resp["expires_at"] == "" {
		t.Error("expected expires_at in response")
	}
}

func TestLoginBadJSON(t *testing.T) {
	h := handlers.NewAuth("http://unused", &stubAppDB{})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d want 400", w.Code)
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
	// Auth server returns 401 for bad credentials.
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"INVALID_CREDENTIALS"}`, http.StatusUnauthorized)
	}))
	defer authSrv.Close()

	h := handlers.NewAuth(authSrv.URL, &stubAppDB{})
	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
