package handlers_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/api/internal/authclient"
	"github.com/veloci/api/internal/handlers"
)

// stubAuthForLogin simulates veloci-auth /credentials/validate and /tokens/mint.
// Responses match the ogen-generated client's expected schema fields.
func stubAuthForLogin(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/credentials/validate":
			json.NewEncoder(w).Encode(map[string]string{
				"credential_id": "cred-1",
				"system_role":   "user",
			})
		case "/tokens/mint":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "test-access-token",
				"refresh_token": "test-refresh-token",
				"jti":           "jti-1",
				"expires_in":    900,
				"expires_at":    "2099-01-01T00:00:00Z",
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

func mustAuthClient(t *testing.T, url string) *authclient.Client {
	t.Helper()
	c, err := authclient.NewClient(url)
	if err != nil {
		t.Fatalf("authclient.NewClient(%q): %v", url, err)
	}
	return c
}

func TestLoginSuccess(t *testing.T) {
	authSrv := stubAuthForLogin(t)
	defer authSrv.Close()

	h := handlers.NewAuth(mustAuthClient(t, authSrv.URL), &stubAppDB{})
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
	h := handlers.NewAuth(mustAuthClient(t, "http://unused"), &stubAppDB{})
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
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"status": 401,
			"title":  "Unauthorized",
			"detail": "invalid credentials",
		})
	}))
	defer authSrv.Close()

	h := handlers.NewAuth(mustAuthClient(t, authSrv.URL), &stubAppDB{})
	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.Login(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
