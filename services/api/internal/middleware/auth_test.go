package middleware_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/api/internal/authclient"
	"github.com/veloci/api/internal/middleware"
)

// mockAuthServer simulates veloci-auth /tokens/validate.
// The mock returns the shape that authclient.ValidateResult expects:
// {"jti":"...","credential_id":"...","claims":{...}}
func mockAuthServer(claims map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/validate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"jti":           "test-jti",
			"credential_id": "cred-1",
			"claims":        claims,
		})
	}))
}

func TestAuthMiddlewareInjectsClaims(t *testing.T) {
	srv := mockAuthServer(map[string]any{
		"sub": "user-1", "entity_id": "ent-1",
		"entity_role": "entity_admin", "system_role": "user",
	})
	defer srv.Close()

	client := authclient.New(srv.URL)
	var gotEntityID, gotEntityRole string

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEntityID = middleware.EntityID(r.Context())
		gotEntityRole = middleware.EntityRole(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sometoken")
	w := httptest.NewRecorder()
	middleware.Authenticate(client)(next).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", w.Code)
	}
	if gotEntityID != "ent-1" {
		t.Errorf("entity_id: got %q", gotEntityID)
	}
	if gotEntityRole != "entity_admin" {
		t.Errorf("entity_role: got %q", gotEntityRole)
	}
}

func TestAuthMiddlewareRejectsMissingToken(t *testing.T) {
	client := authclient.New("http://unused")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	middleware.Authenticate(client)(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
	// Server returns non-200 to simulate invalid token.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"code":"UNAUTHORIZED"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := authclient.New(srv.URL)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	w := httptest.NewRecorder()
	middleware.Authenticate(client)(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}
