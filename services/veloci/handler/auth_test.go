package handler_test

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
	"github.com/veloci/veloci/authclient"
	"github.com/veloci/veloci/handler"
)

func authRouter(authURL string, db handler.AppDB) (*chi.Mux, error) {
	client, err := authclient.NewClient(authURL)
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	api := humachi.New(r, huma.DefaultConfig("test", "1.0.0"))
	handler.RegisterAuthRoutes(api, handler.NewAuthHandler(client, db))
	return r, nil
}

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

type stubAppDB struct{}

func (s *stubAppDB) FindUserEntity(ctx context.Context, email string) (handler.UserEntity, error) {
	return handler.UserEntity{UserID: "user-1", EntityID: "ent-1", EntityRole: "entity_admin"}, nil
}

func TestLoginSuccess(t *testing.T) {
	authSrv := stubAuthForLogin(t)
	defer authSrv.Close()

	r, err := authRouter(authSrv.URL, &stubAppDB{})
	if err != nil {
		t.Fatalf("authRouter: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "pw"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

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
	r, err := authRouter("http://unused", &stubAppDB{})
	if err != nil {
		t.Fatalf("authRouter: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader([]byte("not-json")))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code < 400 {
		t.Errorf("status: got %d want 4xx", w.Code)
	}
}

func TestLoginInvalidCredentials(t *testing.T) {
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

	r, err := authRouter(authSrv.URL, &stubAppDB{})
	if err != nil {
		t.Fatalf("authRouter: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"email": "a@b.com", "password": "wrong"})
	req := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
