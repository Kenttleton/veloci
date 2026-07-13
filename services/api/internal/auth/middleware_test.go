package auth_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/veloci/api/internal/auth"
	"github.com/veloci/api/internal/authclient"
)

func mustAuthClient(t *testing.T, url string) *authclient.Client {
	t.Helper()
	c, err := authclient.NewClient(url)
	if err != nil {
		t.Fatalf("authclient.NewClient(%q): %v", url, err)
	}
	return c
}

func mockAuthServer(claims map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tokens/validate" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token_type":    "access",
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

	client := mustAuthClient(t, srv.URL)
	var gotEntityID, gotEntityRole string

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEntityID = auth.EntityID(r.Context())
		gotEntityRole = auth.EntityRole(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer sometoken")
	w := httptest.NewRecorder()
	auth.Authenticate(client)(next).ServeHTTP(w, req)

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
	client := mustAuthClient(t, "http://unused")
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	auth.Authenticate(client)(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}

func TestAuthMiddlewareRejectsInvalidToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]any{
			"status": 401,
			"title":  "Unauthorized",
			"detail": "invalid token",
		})
	}))
	defer srv.Close()

	client := mustAuthClient(t, srv.URL)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer badtoken")
	w := httptest.NewRecorder()
	auth.Authenticate(client)(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status: got %d want 401", w.Code)
	}
}

func TestAuthMiddlewareRejectsInviteToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"token_type": "invite",
			"claims":     map[string]string{"email": "invited@example.com"},
		})
	}))
	defer srv.Close()

	client := mustAuthClient(t, srv.URL)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer invitetoken")
	w := httptest.NewRecorder()
	auth.Authenticate(client)(next).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("invite token: got %d want 401", w.Code)
	}
}
